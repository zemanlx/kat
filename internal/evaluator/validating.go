package evaluator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	celtypes "github.com/google/cel-go/common/types"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	plugincel "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/matchconditions"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	apiservercel "k8s.io/apiserver/pkg/cel"
	"k8s.io/apiserver/pkg/cel/environment"
)

// ValidationFailureAnnotation is the audit annotation under which the API
// server records the failed validations of bindings with the Audit action.
const ValidationFailureAnnotation = "validation.policy.admission.k8s.io/validation_failure"

const (
	maxAuditAnnotationValueLength = 10 * 1024
	maxValidationFailures         = 50
)

// validationFailure is the API server's validating.ValidationFailureValue.
type validationFailure struct {
	Message           string                            `json:"message"`
	Policy            string                            `json:"policy"`
	Binding           string                            `json:"binding"`
	ExpressionIndex   int                               `json:"expressionIndex"`
	ValidationActions []admissionregv1.ValidationAction `json:"validationActions"`
}

// validator is a compiled ValidatingAdmissionPolicy, as built by the API
// server's validating.compilePolicy.
type validator struct {
	policy           *admissionregv1.ValidatingAdmissionPolicy
	failurePolicy    admissionregv1.FailurePolicyType
	matcher          matchconditions.Matcher
	validations      plugincel.ConditionEvaluator
	messages         plugincel.ConditionEvaluator
	auditAnnotations plugincel.ConditionEvaluator
}

func (e *Evaluator) compileValidating(policy *admissionregv1.ValidatingAdmissionPolicy) (*validator, error) {
	compiler, err := plugincel.NewCompositedCompiler(e.env)
	if err != nil {
		return nil, fmt.Errorf("create CEL compiler: %w", err)
	}

	spec := &policy.Spec
	hasParams := spec.ParamKind != nil
	opts := plugincel.OptionalVariableDeclarations{HasParams: hasParams, HasAuthorizer: true}
	messageOpts := plugincel.OptionalVariableDeclarations{HasParams: hasParams}

	variables := make([]plugincel.NamedExpressionAccessor, len(spec.Variables))
	for i, v := range spec.Variables {
		variables[i] = &namedExpression{name: v.Name, expression: v.Expression}
	}

	compiler.CompileAndStoreVariables(variables, opts, environment.StoredExpressions)

	v := &validator{policy: policy, failurePolicy: failurePolicy(spec.FailurePolicy)}

	if len(spec.MatchConditions) > 0 {
		conditions := make([]plugincel.ExpressionAccessor, len(spec.MatchConditions))
		for i := range spec.MatchConditions {
			conditions[i] = (*matchconditions.MatchCondition)(&spec.MatchConditions[i])
		}

		v.matcher = matchconditions.NewMatcher(compiler.CompileCondition(conditions, opts, environment.StoredExpressions),
			spec.FailurePolicy, "policy", "validate", policy.Name)
	}

	validations := make([]plugincel.ExpressionAccessor, len(spec.Validations))
	messages := make([]plugincel.ExpressionAccessor, len(spec.Validations))

	for i, validation := range spec.Validations {
		validations[i] = &typedExpression{expression: validation.Expression, returnTypes: boolType}
		if validation.MessageExpression != "" {
			messages[i] = &typedExpression{expression: validation.MessageExpression, returnTypes: stringType}
		}
	}

	annotations := make([]plugincel.ExpressionAccessor, len(spec.AuditAnnotations))
	for i, a := range spec.AuditAnnotations {
		annotations[i] = &typedExpression{expression: a.ValueExpression, returnTypes: stringOrNullType}
	}

	v.validations = compiler.CompileCondition(validations, opts, environment.StoredExpressions)
	v.messages = compiler.CompileCondition(messages, messageOpts, environment.StoredExpressions)
	v.auditAnnotations = compiler.CompileCondition(annotations, opts, environment.StoredExpressions)

	return v, nil
}

type decisionAction int

const (
	actionAdmit decisionAction = iota
	actionDeny
)

type decision struct {
	action  decisionAction
	message string
	reason  metav1.StatusReason
}

type auditAnnotationAction int

const (
	annotationExclude auditAnnotationAction = iota
	annotationPublish
	annotationError
)

type auditAnnotationResult struct {
	action auditAnnotationAction
	key    string
	value  string
	err    string
}

func (v *validator) actionForError() decisionAction {
	if v.failurePolicy == admissionregv1.Ignore {
		return actionAdmit
	}

	return actionDeny
}

func (v *validator) errorDecision(message string) []decision {
	return []decision{{action: v.actionForError(), message: message}}
}

// validate is the API server's validating validator.Validate.
func (v *validator) validate(
	ctx context.Context,
	req *request,
	params runtime.Object,
) ([]decision, []auditAnnotationResult) {
	if v.matcher != nil {
		match := v.matcher.Match(ctx, req.attr, params, req.authz)
		if match.Error != nil {
			return v.errorDecision(match.Error.Error()), nil
		}

		if !match.Matches {
			return nil, nil
		}
	}

	optionalVars := plugincel.OptionalVariableBindings{VersionedParams: params, Authorizer: req.authz}
	expressionVars := plugincel.OptionalVariableBindings{VersionedParams: params}
	gvr := req.attr.GetResource()
	admissionRequest := plugincel.CreateAdmissionRequest(req.attr.Attributes, metav1.GroupVersionResource(gvr), metav1.GroupVersionKind(req.attr.VersionedKind))
	ns := plugincel.CreateNamespaceObject(req.namespace)

	results, remainingBudget, err := v.validations.ForInput(ctx, req.attr, admissionRequest, optionalVars, ns, celconfig.RuntimeCELCostBudget)
	if err != nil {
		return v.errorDecision(err.Error()), nil
	}

	decisions := make([]decision, len(results))
	messages, _, messageErr := v.messages.ForInput(ctx, req.attr, admissionRequest, expressionVars, ns, remainingBudget)

	for i, result := range results {
		var message *plugincel.EvaluationResult
		if len(messages) > i {
			message = &messages[i]
		}

		decisions[i] = v.decide(&v.policy.Spec.Validations[i], result, message, messageErr)
	}

	annotations, err := v.evaluateAuditAnnotations(ctx, req, admissionRequest, params)
	if err != nil {
		return v.errorDecision(err.Error()), nil
	}

	return decisions, annotations
}

// decide turns a validation's result into a decision.
func (v *validator) decide(
	validation *admissionregv1.Validation,
	result plugincel.EvaluationResult,
	message *plugincel.EvaluationResult,
	messageErr error,
) decision {
	switch {
	case result.Error != nil:
		return decision{action: v.actionForError(), message: result.Error.Error()}
	case errors.Is(messageErr, apiservercel.ErrInternal) || errors.Is(messageErr, apiservercel.ErrOutOfBudget):
		return decision{action: v.actionForError(), message: fmt.Sprintf("failed messageExpression: %s", messageErr)}
	case result.EvalResult != celtypes.True:
		reason := metav1.StatusReasonInvalid
		if validation.Reason != nil {
			reason = *validation.Reason
		}

		return decision{action: actionDeny, reason: reason, message: failureMessage(validation, message)}
	default:
		return decision{action: actionAdmit}
	}
}

// failureMessage picks a failed validation's message: a usable
// messageExpression result, else message, else the expression itself.
func failureMessage(validation *admissionregv1.Validation, result *plugincel.EvaluationResult) string {
	var message string

	if result != nil && result.Error == nil && result.EvalResult != nil {
		if s, ok := result.EvalResult.Value().(string); ok {
			message = strings.TrimSpace(s)
			if len(message) > celconfig.MaxEvaluatedMessageExpressionSizeBytes || strings.Contains(message, "\n") {
				message = ""
			}
		}
	}

	if message == "" {
		message = strings.TrimSpace(validation.Message)
	}

	if message == "" {
		message = "failed expression: " + strings.TrimSpace(validation.Expression)
	}

	return message
}

func (v *validator) evaluateAuditAnnotations(
	ctx context.Context,
	req *request,
	admissionRequest *admissionv1.AdmissionRequest,
	params runtime.Object,
) ([]auditAnnotationResult, error) {
	vars := plugincel.OptionalVariableBindings{VersionedParams: params}

	results, _, err := v.auditAnnotations.ForInput(ctx, req.attr, admissionRequest, vars, req.namespace, celconfig.RuntimeCELCostBudget)
	if err != nil {
		return nil, err //nolint:wrapcheck // The server denies with this error's message as is.
	}

	errorAction := annotationError
	if v.failurePolicy == admissionregv1.Ignore {
		errorAction = annotationExclude
	}

	annotations := make([]auditAnnotationResult, len(results))

	for i, result := range results {
		a := &annotations[i]
		spec := v.policy.Spec.AuditAnnotations[i]
		a.key = spec.Key

		if result.Error != nil {
			a.action, a.err = errorAction, result.Error.Error()

			continue
		}

		switch result.EvalResult.Type() {
		case celtypes.StringType:
			if value, _ := result.EvalResult.Value().(string); strings.TrimSpace(value) != "" {
				a.action, a.value = annotationPublish, strings.TrimSpace(value)
			}
		case celtypes.NullType:
		default:
			a.action = annotationError
			a.err = fmt.Sprintf("valueExpression '%v' resulted in unsupported return type: %v. "+
				"Return type must be either string or null.", spec.ValueExpression, result.EvalResult.Type())
		}
	}

	return annotations, nil
}

// validatingOutcome collects one policy's decisions across its bindings, as
// the API server's validating dispatcher does.
type validatingOutcome struct {
	denied   []deniedDecision
	warnings []string
	// warned holds the server's full warnings, which name the binding.
	warned      map[string]bool
	failures    []validationFailure
	annotations map[string][]string
	keys        []string
}

type deniedDecision struct {
	decision

	binding string
}

func (o *validatingOutcome) addAnnotation(key, value string) {
	if len(value) > maxAuditAnnotationValueLength {
		value = value[:maxAuditAnnotationValueLength]
	}

	if o.annotations == nil {
		o.annotations = map[string][]string{}
	}

	if _, ok := o.annotations[key]; !ok {
		o.keys = append(o.keys, key)
	}

	if !slices.Contains(o.annotations[key], value) {
		o.annotations[key] = append(o.annotations[key], value)
	}
}

// addWarning records a failure message the server returns as the warning
// "Validation failed for ValidatingAdmissionPolicy '<policy>' with binding
// '<binding>': <message>". The server drops duplicate warnings.
func (o *validatingOutcome) addWarning(policy, binding, message string) {
	warning := fmt.Sprintf("Validation failed for ValidatingAdmissionPolicy '%s' with binding '%s': %s", policy, binding, message)
	if o.warned[warning] {
		return
	}

	if o.warned == nil {
		o.warned = map[string]bool{}
	}

	o.warned[warning] = true
	o.warnings = append(o.warnings, message)
}

func (e *Evaluator) evaluateValidating(
	ctx context.Context,
	policy *admissionregv1.ValidatingAdmissionPolicy,
	bindings []*admissionregv1.ValidatingAdmissionPolicyBinding,
	req *request,
) (*EvaluationResult, error) {
	v, err := e.compileValidating(policy)
	if err != nil {
		return nil, err
	}

	out := &validatingOutcome{}

	configError := func(binding string, err error) {
		if v.failurePolicy == admissionregv1.Fail {
			out.deny(binding, "failed to configure binding: "+err.Error())
		}
	}

	for _, binding := range bindings {
		applies, err := policyApplies(policy.Spec.MatchConstraints, binding.Spec.MatchResources, req.attr, req.namespaceObj)
		if err != nil {
			configError(binding.Name, err)

			continue
		}

		if !applies {
			continue
		}

		params, err := collectParams(policy.Spec.ParamKind, binding.Spec.ParamRef, req.params)
		if err != nil {
			configError(binding.Name, err)

			continue
		}

		for _, p := range params {
			decisions, annotations := v.validate(ctx, req, p)
			out.record(policy.Name, binding, decisions, annotations)
		}
	}

	return out.result(), nil
}

func (o *validatingOutcome) record(
	policy string,
	binding *admissionregv1.ValidatingAdmissionPolicyBinding,
	decisions []decision,
	annotations []auditAnnotationResult,
) {
	for i, d := range decisions {
		if d.action == actionDeny {
			o.recordFailure(policy, binding, i, d)
		}
	}

	for _, a := range annotations {
		switch a.action {
		case annotationPublish:
			o.addAnnotation(a.key, a.value)
		case annotationError:
			o.deny(binding.Name, a.err)
		case annotationExclude:
		}
	}
}

func (o *validatingOutcome) deny(binding, message string) {
	o.denied = append(o.denied, deniedDecision{action: actionDeny, message: message, binding: binding})
}

// recordFailure applies the binding's validation actions to a failed validation.
func (o *validatingOutcome) recordFailure(policy string, binding *admissionregv1.ValidatingAdmissionPolicyBinding, index int, d decision) {
	for _, action := range binding.Spec.ValidationActions {
		switch action {
		case admissionregv1.Deny:
			o.denied = append(o.denied, deniedDecision{decision: d, binding: binding.Name})
		case admissionregv1.Audit:
			o.failures = append(o.failures, validationFailure{
				Message:           d.message,
				Policy:            binding.Spec.PolicyName,
				Binding:           binding.Name,
				ExpressionIndex:   index,
				ValidationActions: binding.Spec.ValidationActions,
			})
		case admissionregv1.Warn:
			o.addWarning(policy, binding.Name, d.message)
		}
	}
}

func (o *validatingOutcome) result() *EvaluationResult {
	result := &EvaluationResult{Allowed: true, Warnings: o.warnings}

	for _, key := range o.keys {
		if result.AuditAnnotations == nil {
			result.AuditAnnotations = map[string]string{}
		}

		// The server publishes the annotation as "<policy>/<key>"; kat tests assert the key alone.
		result.AuditAnnotations[key] = strings.Join(o.annotations[key], ", ")
	}

	if failures := o.failures; len(failures) > 0 {
		if len(failures) > maxValidationFailures {
			failures = failures[:maxValidationFailures]
		}

		if value, err := json.Marshal(failures); err == nil {
			if result.AuditAnnotations == nil {
				result.AuditAnnotations = map[string]string{}
			}

			result.AuditAnnotations[ValidationFailureAnnotation] = string(value)
		}
	}

	if len(o.denied) > 0 {
		denied := o.denied[0]
		result.Allowed = false
		result.Message, result.Binding = denied.message, denied.binding

		result.Reason = denied.reason
		if result.Reason == "" {
			result.Reason = metav1.StatusReasonInvalid
		}
	}

	return result
}
