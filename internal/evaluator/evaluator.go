package evaluator

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/ext"
	"github.com/pmezard/go-difflib/difflib"
	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v3"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	admissionv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	plugin "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	celcommon "k8s.io/apiserver/pkg/cel/common"
	"k8s.io/apiserver/pkg/cel/library"
	"k8s.io/apiserver/pkg/cel/mutation"
	"k8s.io/apiserver/pkg/cel/mutation/dynamic"
	"k8s.io/utils/ptr"
)

var (
	errMutatingRequiresObject   = errors.New("mutating policy requires object or oldObject")
	errUnsupportedPatchType     = errors.New("unsupported patch type")
	errValidationNonBoolean     = errors.New("validation expression returned non-boolean")
	errMatchConditionNonBoolean = errors.New("match condition returned non-boolean")
	errApplyConfigNotObject     = errors.New("apply configuration must return Object")
	errConvertCELUnexpectedType = errors.New("convertCELValue returned unexpected type")
	errUnexpectedPatchType      = errors.New("unexpected patch type")
	errConversionNotSupported   = errors.New("conversion not supported")
	errNoPolicy                 = errors.New("no policy provided")
)

const diffContextLines = 3

// Evaluator evaluates admission policies using CEL expressions.
type Evaluator struct {
	env *cel.Env
}

// New creates a new Evaluator with a CEL environment configured for Kubernetes admission policies.
func New() (*Evaluator, error) {
	// Build environment options with all Kubernetes CEL libraries
	envOpts := []cel.EnvOption{
		cel.Variable(plugin.ObjectVarName, cel.DynType),
		cel.Variable(plugin.OldObjectVarName, cel.DynType),
		cel.Variable(plugin.RequestVarName, cel.DynType),
		cel.Variable(plugin.ParamsVarName, cel.DynType),
		cel.Variable(plugin.NamespaceVarName, cel.DynType),
		cel.Variable(plugin.AuthorizerVarName, cel.DynType),
		// Add all Kubernetes CEL function libraries
		library.Authz(),
		library.AuthzSelectors(),
		library.CIDR(),   // CIDR parsing and operations
		library.Format(), // String formatting
		library.IP(),     // IP address operations
		library.JSONPatch(),
		library.Lists(),
		library.Quantity(), // Kubernetes quantity parsing (e.g., "100Mi", "2Gi")
		library.Regex(),
		library.SemverLib(), // Semantic version comparison
		library.URLs(),
		// Add CEL standard extensions for maximum compatibility
		ext.Strings(),  // split(), replace(), substring(), trim(), etc.
		ext.Lists(),    // Additional list operations
		ext.Math(),     // Math operations (min, max, etc.)
		ext.Encoders(), // Base64 encoding/decoding
		ext.Sets(),     // Set operations (sets.contains, etc.)
		// Add type resolver for JSONPatch and Object types (for mutations)
		celcommon.ResolverEnvOption(&mutation.DynamicTypeResolver{}),
	}

	env, err := cel.NewEnv(envOpts...)
	if err != nil {
		return nil, fmt.Errorf("create CEL environment: %w", err)
	}

	return &Evaluator{env: env}, nil
}

// TestCase represents a test case with inputs and expected outcomes.
// This is a subset of the loader.TestCase interface that evaluator needs.
//
//nolint:interfacebloat // Wrapper interface for test cases
type TestCase interface {
	GetRequest() *admissionv1.AdmissionRequest
	GetObject() *unstructured.Unstructured
	GetOldObject() *unstructured.Unstructured
	GetParams() *unstructured.Unstructured
	GetNamespaceObj() *unstructured.Unstructured
	GetUserInfo() user.Info
	GetExpectAllowed() bool
	GetExpectMessage() string
	GetExpectWarnings() []string
	GetExpectAuditAnnotations() map[string]string
	GetExpectedObject() *unstructured.Unstructured
	GetError() error
	GetAuthorizer() []AuthorizationMockConfig
}

// EvaluateTest evaluates a policy against a test case and returns whether it passed.
func (e *Evaluator) EvaluateTest(
	mutatingPolicy *admissionv1beta1.MutatingAdmissionPolicy,
	mutatingBinding *admissionv1beta1.MutatingAdmissionPolicyBinding,
	validatingPolicy *admissionregv1.ValidatingAdmissionPolicy,
	validatingBinding *admissionregv1.ValidatingAdmissionPolicyBinding,
	testCase TestCase,
) *TestResult {
	expected := TestExpectation{
		Allowed:          testCase.GetExpectAllowed(),
		Message:          testCase.GetExpectMessage(),
		Object:           testCase.GetExpectedObject(),
		Warnings:         testCase.GetExpectWarnings(),
		AuditAnnotations: testCase.GetExpectAuditAnnotations(),
	}

	// Check for loading errors first
	if err := testCase.GetError(); err != nil {
		return &TestResult{
			Passed:   false,
			Expected: expected,
			Message:  fmt.Sprintf("test loading error: %v", err),
		}
	}

	// Evaluate policy
	evalResult, err := e.evaluatePolicy(mutatingPolicy, mutatingBinding, validatingPolicy, validatingBinding, testCase)
	if err != nil {
		return &TestResult{
			Passed:   false,
			Expected: expected,
			Message:  fmt.Sprintf("evaluation error: %v", err),
		}
	}

	if evalResult == nil {
		return &TestResult{
			Passed:   false,
			Expected: expected,
			Message:  "no policy provided",
		}
	}

	// Populate actual outcome
	actual := TestOutcome{
		Allowed:          evalResult.Allowed,
		Message:          evalResult.Message,
		Warnings:         evalResult.Warnings,
		AuditAnnotations: evalResult.AuditAnnotations,
	}

	if evalResult.PatchedObject != nil {
		actual.Object = evalResult.PatchedObject
	} else {
		actual.Object = testCase.GetObject()
	}

	// Compare expected vs actual
	result := &TestResult{
		Expected:      expected,
		Actual:        actual,
		PatchedObject: evalResult.PatchedObject,
	}

	return validateTestResult(result, &expected, &actual)
}

func validateTestResult(result *TestResult, expected *TestExpectation, actual *TestOutcome) *TestResult {
	// Check if test passed with early returns
	if actual.Allowed != expected.Allowed {
		result.Passed = false
		result.Message = fmt.Sprintf("expected allowed=%v, got allowed=%v", expected.Allowed, actual.Allowed)

		return result
	}

	if chk := checkAuditAnnotations(expected, actual); chk != nil {
		return chk
	}

	if chk := checkWarnings(expected.Warnings, actual.Warnings); chk != nil {
		result.Passed = false
		result.Message = chk.Message

		return result
	}

	if expected.Message != "" && actual.Message != expected.Message {
		result.Passed = false

		// Use a diff to make it easier to see differences
		diff := getDiff(expected.Message, actual.Message)

		if diff != "" {
			result.Message = "message does not match expected:\n" + diff
		} else {
			result.Message = fmt.Sprintf("expected message %q, got %q", expected.Message, actual.Message)
		}

		return result
	}

	if chk := checkMutatedObject(expected, actual); chk != nil {
		return chk
	}

	result.Passed = true

	return result
}

// getDiff returns a unified diff string between expected and actual values.
func getDiff(expected, actual string) string {
	diff, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(expected),
		B:        difflib.SplitLines(actual),
		FromFile: "Expected",
		ToFile:   "Actual",
		Context:  diffContextLines,
	})

	return diff
}

// evaluatePolicy evaluates the appropriate policy (mutating or validating) and returns the result.
func (e *Evaluator) evaluatePolicy(
	mutatingPolicy *admissionv1beta1.MutatingAdmissionPolicy,
	mutatingBinding *admissionv1beta1.MutatingAdmissionPolicyBinding,
	validatingPolicy *admissionregv1.ValidatingAdmissionPolicy,
	validatingBinding *admissionregv1.ValidatingAdmissionPolicyBinding,
	testCase TestCase,
) (*EvaluationResult, error) {
	// Create mock authorizer if configured
	var auth authorizer.Authorizer
	if configs := testCase.GetAuthorizer(); len(configs) > 0 {
		auth = NewMockAuthorizerFromConfig(configs)
	}

	switch {
	case mutatingPolicy != nil:
		return e.EvaluateMutating(
			mutatingPolicy,
			mutatingBinding,
			testCase.GetRequest(),
			testCase.GetObject(),
			testCase.GetOldObject(),
			testCase.GetParams(),
			testCase.GetNamespaceObj(),
			auth,
			testCase.GetUserInfo(),
		)
	case validatingPolicy != nil:
		return e.EvaluateValidating(
			validatingPolicy,
			validatingBinding,
			testCase.GetRequest(),
			testCase.GetObject(),
			testCase.GetOldObject(),
			testCase.GetParams(),
			testCase.GetNamespaceObj(),
			auth,
			testCase.GetUserInfo(),
		)
	default:
		return nil, errNoPolicy
	}
}

// checkWarnings verifies that actual warnings match expected warnings.
// Returns a TestResult on mismatch, or nil if all checks pass.
func checkWarnings(expected, actual []string) *TestResult {
	if len(expected) == 0 {
		return nil
	}

	if len(actual) == 0 {
		return &TestResult{
			Passed:  false,
			Message: fmt.Sprintf("expected warnings %v, got none", expected),
		}
	}

	if len(actual) != len(expected) {
		return &TestResult{
			Passed:  false,
			Message: fmt.Sprintf("expected %d warnings, got %d", len(expected), len(actual)),
		}
	}

	for i, expectedWarning := range expected {
		if actual[i] != expectedWarning {
			diff := getDiff(expectedWarning, actual[i])
			if diff != "" {
				return &TestResult{
					Passed:  false,
					Message: fmt.Sprintf("warning[%d] does not match expected:\n%s", i, diff),
				}
			}

			return &TestResult{
				Passed:  false,
				Message: fmt.Sprintf("warning[%d]: expected %q, got %q", i, expectedWarning, actual[i]),
			}
		}
	}

	return nil
}

// checkAuditAnnotations verifies that actual audit annotations match expected ones.
// Returns a TestResult on mismatch, or nil if all checks pass.
func checkAuditAnnotations(expected *TestExpectation, actual *TestOutcome) *TestResult {
	if len(expected.AuditAnnotations) == 0 {
		return nil
	}

	result := &TestResult{}

	// Filter actual to only contain keys from expected, to ignore extra annotations
	actualFiltered := make(map[string]string)

	if actual.AuditAnnotations != nil {
		for k := range expected.AuditAnnotations {
			if v, ok := actual.AuditAnnotations[k]; ok {
				actualFiltered[k] = v
			}
		}
	}

	if !reflect.DeepEqual(expected.AuditAnnotations, actualFiltered) {
		result.Passed = false

		expectedYAML, err := yaml.Marshal(expected.AuditAnnotations)
		if err != nil {
			expectedYAML = []byte(fmt.Sprintf("%+v", expected.AuditAnnotations))
		}

		actualYAML, err := yaml.Marshal(actualFiltered)
		if err != nil {
			actualYAML = []byte(fmt.Sprintf("%+v", actualFiltered))
		}

		diff := getDiff(string(expectedYAML), string(actualYAML))
		if diff == "" {
			diff = fmt.Sprintf("Expected:\n%s\nActual:\n%s", string(expectedYAML), string(actualYAML))
		}

		result.Message = "audit annotations do not match expected:\n" + diff

		return result
	}

	return nil
}

// checkMutatedObject verifies that actual object matches expected mutated object.
// Returns a TestResult on mismatch, or nil if all checks pass.
func checkMutatedObject(expected *TestExpectation, actual *TestOutcome) *TestResult {
	if expected.Object == nil {
		return nil
	}

	result := &TestResult{}

	if actual.Object == nil {
		result.Passed = false
		result.Message = "expected mutated object, got none"

		return result
	}

	// Compare objects - they should match exactly
	if !reflect.DeepEqual(expected.Object.Object, actual.Object.Object) {
		result.Passed = false

		// Convert to YAML for consistent diffing
		expectedYAML, err := yaml.Marshal(expected.Object.Object)
		if err != nil {
			expectedYAML = []byte(fmt.Sprintf("%+v", expected.Object.Object))
		}

		actualYAML, err := yaml.Marshal(actual.Object.Object)
		if err != nil {
			actualYAML = []byte(fmt.Sprintf("%+v", actual.Object.Object))
		}

		// Generate a standard unified diff
		diff := getDiff(string(expectedYAML), string(actualYAML))

		// If difflib fails to produce a diff (e.g. only whitespace differs or identical content but DeepEqual failed),
		// fallback to simple string/YAML mismatch message.
		if diff == "" {
			diff = fmt.Sprintf("Expected:\n%s\nActual:\n%s", string(expectedYAML), string(actualYAML))
		}

		result.Message = "mutated object does not match expected:\n" + diff

		return result
	}

	return nil
}

// handleValidationFailure handles the case when validation fails, determining the appropriate action.
func (e *Evaluator) handleValidationFailure(validation *admissionregv1.Validation, binding *admissionregv1.ValidatingAdmissionPolicyBinding, auditAnnotations map[string]string, vars map[string]any) (*EvaluationResult, error) {
	message := validation.Message

	// If messageExpression is provided, evaluate it
	if validation.MessageExpression != "" {
		msgResult, err := e.evaluateExpression(validation.MessageExpression, vars)
		if err != nil {
			return nil, fmt.Errorf("evaluate messageExpression %q: %w", validation.MessageExpression, err)
		}

		if msgStr, ok := msgResult.(string); ok && strings.TrimSpace(msgStr) != "" {
			message = msgStr
		}
	}

	if message == "" {
		message = "validation failed: " + validation.Expression
	}

	// Check binding actions
	action := e.getValidationAction(binding)
	switch action {
	case admissionregv1.Warn:
		return &EvaluationResult{
			Allowed:          true,
			Warnings:         []string{message},
			AuditAnnotations: auditAnnotations,
		}, nil
	case admissionregv1.Audit:
		return &EvaluationResult{
			Allowed:          true,
			AuditAnnotations: auditAnnotations,
		}, nil
	case admissionregv1.Deny:
		fallthrough
	default:
		return &EvaluationResult{
			Allowed:          false,
			Message:          message,
			AuditAnnotations: auditAnnotations,
		}, nil
	}
}

// getValidationAction returns the validation action to take (Warn, Audit, or Deny).
func (e *Evaluator) getValidationAction(binding *admissionregv1.ValidatingAdmissionPolicyBinding) admissionregv1.ValidationAction {
	if binding == nil || len(binding.Spec.ValidationActions) == 0 {
		return ""
	}

	return binding.Spec.ValidationActions[0]
}

// setupValidatingVars sets up CEL variables for validating evaluation.
func (e *Evaluator) setupValidatingVars(
	requestMap map[string]any,
	object, oldObject, params, namespaceObj *unstructured.Unstructured,
	authorizer authorizer.Authorizer,
	userInfo user.Info,
) map[string]any {
	vars := map[string]any{
		plugin.RequestVarName: requestMap,
	}

	// For connect/delete/other operations, bind 'object' variable appropriately
	switch {
	case object != nil:
		vars[plugin.ObjectVarName] = object.Object
	case oldObject != nil:
		vars[plugin.ObjectVarName] = oldObject.Object
	default:
		vars[plugin.ObjectVarName] = nil
	}

	// Always add params (as null if not provided) so CEL can check for null
	if params != nil {
		vars[plugin.ParamsVarName] = params.Object
	} else {
		vars[plugin.ParamsVarName] = nil
	}

	if authorizer != nil && userInfo != nil {
		vars[plugin.AuthorizerVarName] = NewAuthorizerValue(authorizer, userInfo)
	}

	if oldObject != nil {
		vars[plugin.OldObjectVarName] = oldObject.Object
	}

	if namespaceObj != nil {
		vars[plugin.NamespaceVarName] = namespaceObj.Object
	}

	return vars
}

// evaluateAuditAnnotations evaluates all audit annotations and returns them as a map.
func (e *Evaluator) evaluateAuditAnnotations(annotations []admissionregv1.AuditAnnotation, vars map[string]any) (map[string]string, error) {
	auditAnnotations := make(map[string]string)

	for _, annotation := range annotations {
		value, err := e.evaluateExpression(annotation.ValueExpression, vars)
		if err != nil {
			return nil, fmt.Errorf("evaluate audit annotation %q: %w", annotation.Key, err)
		}
		// Convert value to string
		if strValue, ok := value.(string); ok && strValue != "" {
			auditAnnotations[annotation.Key] = strValue
		}
	}

	return auditAnnotations, nil
}

// EvaluationResult contains the result of evaluating a policy.
type EvaluationResult struct {
	Allowed          bool
	Message          string
	Warnings         []string
	PatchType        *admissionv1.PatchType
	PatchedObject    *unstructured.Unstructured // The object after applying mutations
	AuditAnnotations map[string]string
}

// TestResult contains the result of evaluating a test case.
type TestResult struct {
	Passed        bool
	Expected      TestExpectation
	Actual        TestOutcome
	Message       string // Failure explanation or diff
	PatchedObject *unstructured.Unstructured
}

// TestExpectation contains what the test expects to happen.
type TestExpectation struct {
	Allowed          bool
	Message          string
	Object           *unstructured.Unstructured
	Warnings         []string
	AuditAnnotations map[string]string
}

// TestOutcome contains what actually happened during evaluation.
type TestOutcome struct {
	Allowed          bool
	Message          string
	Object           *unstructured.Unstructured
	Warnings         []string
	AuditAnnotations map[string]string
	EvaluationErr    error
}

// EvaluateMutating evaluates a MutatingAdmissionPolicy against an admission request.
func (e *Evaluator) EvaluateMutating(
	policy *admissionv1beta1.MutatingAdmissionPolicy,
	binding *admissionv1beta1.MutatingAdmissionPolicyBinding,
	request *admissionv1.AdmissionRequest,
	object *unstructured.Unstructured,
	oldObject *unstructured.Unstructured,
	params *unstructured.Unstructured,
	namespaceObj *unstructured.Unstructured,
	authorizer authorizer.Authorizer,
	userInfo user.Info,
) (*EvaluationResult, error) {
	// Evaluate binding's namespaceSelector if present
	if matched, err := e.matchesNamespaceSelectorV1Beta1(binding, namespaceObj); err != nil {
		return nil, fmt.Errorf("evaluate namespace selector: %w", err)
	} else if !matched {
		// Namespace selector doesn't match, policy doesn't apply
		return &EvaluationResult{Allowed: true}, nil
	}

	requestMap, err := convertAdmissionRequest(request)
	if err != nil {
		return nil, fmt.Errorf("convert admission request: %w", err)
	}

	primaryObject := getPrimaryObject(object, oldObject)
	if primaryObject == nil {
		return nil, errMutatingRequiresObject
	}

	vars := prepareMutatingVars(requestMap, primaryObject, oldObject, params, namespaceObj, authorizer, userInfo)

	matched, err := e.evaluateMatchConditionsV1Beta1(policy.Spec.MatchConditions, vars)
	if err != nil {
		return nil, fmt.Errorf("evaluate match conditions: %w", err)
	}

	if !matched {
		return &EvaluationResult{Allowed: true}, nil
	}

	patchedObject, err := e.applyMutations(policy.Spec.Mutations, object, vars)
	if err != nil {
		return nil, err
	}

	return &EvaluationResult{
		Allowed:       true,
		PatchedObject: patchedObject,
	}, nil
}

func getPrimaryObject(object, oldObject *unstructured.Unstructured) *unstructured.Unstructured {
	// For DELETE operations, oldObject is used as the primary object
	// For other operations, object is required
	if object != nil {
		return object
	}

	return oldObject
}

func prepareMutatingVars(
	requestMap map[string]any,
	primaryObject *unstructured.Unstructured,
	oldObject *unstructured.Unstructured,
	params *unstructured.Unstructured,
	namespaceObj *unstructured.Unstructured,
	authorizer authorizer.Authorizer,
	userInfo user.Info,
) map[string]any {
	vars := map[string]any{
		plugin.ObjectVarName:  primaryObject.Object,
		plugin.RequestVarName: requestMap,
	}

	// Always add params (as null if not provided) so CEL can check for null
	if params != nil {
		vars[plugin.ParamsVarName] = params.Object
	} else {
		vars[plugin.ParamsVarName] = nil
	}

	if authorizer != nil && userInfo != nil {
		vars[plugin.AuthorizerVarName] = NewAuthorizerValue(authorizer, userInfo)
	}

	if oldObject != nil {
		vars[plugin.OldObjectVarName] = oldObject.Object
	}

	if namespaceObj != nil {
		vars[plugin.NamespaceVarName] = namespaceObj.Object
	}

	return vars
}

func (e *Evaluator) applyMutations(
	mutations []admissionv1beta1.Mutation,
	object *unstructured.Unstructured,
	vars map[string]any,
) (*unstructured.Unstructured, error) {
	patchedObject := object.DeepCopy()

	for _, mutation := range mutations {
		switch mutation.PatchType {
		case admissionv1beta1.PatchTypeJSONPatch:
			patch, err := e.evaluateJSONPatchMutation(mutation, vars)
			if err != nil {
				return nil, err
			}

			if patch != nil {
				var err error

				patchedObject, err = e.applyJSONPatches([]any{patch}, patchedObject)
				if err != nil {
					return nil, err
				}
			}
		case admissionv1beta1.PatchTypeApplyConfiguration:
			config, err := e.evaluateApplyConfigurationMutation(mutation, vars)
			if err != nil {
				return nil, err
			}

			if config != nil {
				patchedObject = e.applyApplyConfigurations([]*unstructured.Unstructured{config}, patchedObject)
			}
		default:
			return nil, fmt.Errorf("%w: %s", errUnsupportedPatchType, mutation.PatchType)
		}
	}

	return patchedObject, nil
}

// EvaluateValidating evaluates a ValidatingAdmissionPolicy against an admission request.
func (e *Evaluator) EvaluateValidating( //nolint:cyclop // Complexity is inherent in evaluating all aspects of a validating policy
	policy *admissionregv1.ValidatingAdmissionPolicy,
	binding *admissionregv1.ValidatingAdmissionPolicyBinding,
	request *admissionv1.AdmissionRequest,
	object *unstructured.Unstructured,
	oldObject *unstructured.Unstructured,
	params *unstructured.Unstructured,
	namespaceObj *unstructured.Unstructured,
	authorizer authorizer.Authorizer,
	userInfo user.Info,
) (*EvaluationResult, error) {
	// Evaluate binding's namespaceSelector if present
	if matched, err := e.matchesNamespaceSelector(binding, namespaceObj); err != nil {
		return nil, fmt.Errorf("evaluate namespace selector: %w", err)
	} else if !matched {
		// Namespace selector doesn't match, policy doesn't apply
		return &EvaluationResult{Allowed: true}, nil
	}

	// Convert admission request
	requestMap, err := convertAdmissionRequest(request)
	if err != nil {
		return nil, fmt.Errorf("convert admission request: %w", err)
	}

	// Set up CEL variables
	vars := e.setupValidatingVars(requestMap, object, oldObject, params, namespaceObj, authorizer, userInfo)

	// Evaluate matchConditions if present
	matched, err := e.evaluateMatchConditions(policy.Spec.MatchConditions, vars)
	if err != nil {
		return nil, fmt.Errorf("evaluate match conditions: %w", err)
	}

	if !matched {
		// Policy doesn't match, allow
		return &EvaluationResult{Allowed: true}, nil
	}

	// Evaluate audit annotations
	auditAnnotations, err := e.evaluateAuditAnnotations(policy.Spec.AuditAnnotations, vars)
	if err != nil {
		return nil, err
	}

	// Evaluate validations
	for _, validation := range policy.Spec.Validations {
		result, err := e.evaluateExpression(validation.Expression, vars)
		if err != nil {
			return nil, fmt.Errorf("evaluate validation expression %q: %w", validation.Expression, err)
		}

		// If validation returns false, deny
		allowed, ok := result.(bool)
		if !ok {
			return nil, fmt.Errorf("%w: %s returned %T", errValidationNonBoolean, validation.Expression, result)
		}

		if !allowed {
			return e.handleValidationFailure(&validation, binding, auditAnnotations, vars)
		}
	}

	return &EvaluationResult{
		Allowed:          true,
		AuditAnnotations: auditAnnotations,
	}, nil
}

// matchesNamespaceSelector checks if the namespace object's labels match the binding's namespace selector.
// Returns true if the selector matches (policy should be evaluated), false otherwise.
func (e *Evaluator) matchesNamespaceSelector(
	binding *admissionregv1.ValidatingAdmissionPolicyBinding,
	namespaceObj *unstructured.Unstructured,
) (bool, error) {
	// No binding or no matchResources means match all
	if binding == nil || binding.Spec.MatchResources == nil || binding.Spec.MatchResources.NamespaceSelector == nil {
		return true, nil
	}

	// Convert LabelSelector to labels.Selector
	selector, err := metav1.LabelSelectorAsSelector(binding.Spec.MatchResources.NamespaceSelector)
	if err != nil {
		return false, fmt.Errorf("parse namespace selector: %w", err)
	}

	// Empty selector matches everything
	if selector.Empty() {
		return true, nil
	}

	// No namespace object provided - can't evaluate selector
	if namespaceObj == nil {
		return true, nil
	}

	// Get labels from namespace object
	nsLabels := labels.Set(namespaceObj.GetLabels())

	// Check if namespace labels match the selector
	return selector.Matches(nsLabels), nil
}

// matchesNamespaceSelectorV1Beta1 checks if the namespace object's labels match the binding's namespace selector.
// Returns true if the selector matches (policy should be evaluated), false otherwise.
func (e *Evaluator) matchesNamespaceSelectorV1Beta1(
	binding *admissionv1beta1.MutatingAdmissionPolicyBinding,
	namespaceObj *unstructured.Unstructured,
) (bool, error) {
	// No binding or no matchResources means match all
	if binding == nil || binding.Spec.MatchResources == nil || binding.Spec.MatchResources.NamespaceSelector == nil {
		return true, nil
	}

	// Convert LabelSelector to labels.Selector
	selector, err := metav1.LabelSelectorAsSelector(binding.Spec.MatchResources.NamespaceSelector)
	if err != nil {
		return false, fmt.Errorf("parse namespace selector: %w", err)
	}

	// Empty selector matches everything
	if selector.Empty() {
		return true, nil
	}

	// No namespace object provided - can't evaluate selector
	if namespaceObj == nil {
		return true, nil
	}

	// Get labels from namespace object
	nsLabels := labels.Set(namespaceObj.GetLabels())

	// Check if namespace labels match the selector
	return selector.Matches(nsLabels), nil
}

// evaluateMatchConditions evaluates all match conditions and returns true if all match.
func (e *Evaluator) evaluateMatchConditions(conditions []admissionregv1.MatchCondition, vars map[string]any) (bool, error) {
	for _, condition := range conditions {
		result, err := e.evaluateExpression(condition.Expression, vars)
		if err != nil {
			return false, fmt.Errorf("evaluate match condition %q: %w", condition.Name, err)
		}

		matched, ok := result.(bool)
		if !ok {
			return false, fmt.Errorf("%w: %s returned %T", errMatchConditionNonBoolean, condition.Name, result)
		}

		if !matched {
			return false, nil
		}
	}

	return true, nil
}

// evaluateMatchConditionsV1Beta1 evaluates v1beta1 match conditions.
func (e *Evaluator) evaluateMatchConditionsV1Beta1(conditions []admissionv1beta1.MatchCondition, vars map[string]any) (bool, error) {
	for _, condition := range conditions {
		result, err := e.evaluateExpression(condition.Expression, vars)
		if err != nil {
			return false, fmt.Errorf("evaluate match condition %q: %w", condition.Name, err)
		}

		matched, ok := result.(bool)
		if !ok {
			return false, fmt.Errorf("%w: %s returned %T", errMatchConditionNonBoolean, condition.Name, result)
		}

		if !matched {
			return false, nil
		}
	}

	return true, nil
}

// evaluateExpression evaluates a single CEL expression with the given variables.
func (e *Evaluator) evaluateExpression(expression string, vars map[string]any) (any, error) {
	celVal, err := e.evaluateExpressionRaw(expression, vars)
	if err != nil {
		return nil, err
	}

	return celVal.Value(), nil
}

// evaluateExpressionRaw evaluates a CEL expression and returns the raw CEL value without unwrapping.
func (e *Evaluator) evaluateExpressionRaw(expression string, vars map[string]any) (ref.Val, error) {
	ast, issues := e.env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compile expression: %w", issues.Err())
	}

	prg, err := e.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("create program: %w", err)
	}

	result, _, err := prg.Eval(vars)
	if err != nil {
		return nil, fmt.Errorf("evaluate: %w", err)
	}

	return result, nil
}

// convertAdmissionRequest converts an AdmissionRequest to a map for CEL evaluation.
//
//nolint:cyclop,funlen // Field mapping function
func convertAdmissionRequest(req *admissionv1.AdmissionRequest) (map[string]any, error) {
	if req == nil {
		return map[string]any{}, nil
	}

	result := make(map[string]any)

	// Add simple fields
	if req.UID != "" {
		result["uid"] = string(req.UID)
	}

	if req.SubResource != "" {
		result["subResource"] = req.SubResource
	}

	if req.Name != "" {
		result["name"] = req.Name
	}

	if req.Namespace != "" {
		result["namespace"] = req.Namespace
	}

	if req.Operation != "" {
		result["operation"] = string(req.Operation)
	}

	// Add Kind if present
	if req.Kind.Kind != "" {
		result["kind"] = map[string]any{
			"group":   req.Kind.Group,
			"version": req.Kind.Version,
			"kind":    req.Kind.Kind,
		}
	}

	// Add Resource if present
	if req.Resource.Resource != "" {
		result["resource"] = map[string]any{
			"group":    req.Resource.Group,
			"version":  req.Resource.Version,
			"resource": req.Resource.Resource,
		}
	}

	// Add UserInfo if present
	if req.UserInfo.Username != "" || len(req.UserInfo.Groups) > 0 {
		userInfo := make(map[string]any)
		if req.UserInfo.Username != "" {
			userInfo["username"] = req.UserInfo.Username
		}

		if len(req.UserInfo.Groups) > 0 {
			userInfo["groups"] = req.UserInfo.Groups
		}

		if req.UserInfo.UID != "" {
			userInfo["uid"] = req.UserInfo.UID
		}

		if len(req.UserInfo.Extra) > 0 {
			userInfo["extra"] = req.UserInfo.Extra
		}

		result["userInfo"] = userInfo
	}

	// Handle Options RawExtension if present
	if req.Options.Raw != nil {
		var optionsMap map[string]any
		if err := json.Unmarshal(req.Options.Raw, &optionsMap); err != nil {
			if err := yaml.Unmarshal(req.Options.Raw, &optionsMap); err != nil {
				return nil, fmt.Errorf("unmarshal options: %w", err)
			}
		}

		result["options"] = optionsMap
	}

	if req.DryRun != nil {
		result["dryRun"] = *req.DryRun
	}

	return result, nil
}

// evaluateJSONPatchMutation evaluates a JSONPatch mutation and returns the patch result.
func (e *Evaluator) evaluateJSONPatchMutation(
	mutation admissionv1beta1.Mutation,
	vars map[string]any,
) (any, error) {
	if mutation.JSONPatch == nil {
		//nolint:nilnil // No patch to evaluate, no error
		return nil, nil
	}

	patchResult, err := e.evaluateExpression(mutation.JSONPatch.Expression, vars)
	if err != nil {
		return nil, fmt.Errorf("evaluate JSONPatch expression: %w", err)
	}

	return patchResult, nil
}

// evaluateApplyConfigurationMutation evaluates an ApplyConfiguration mutation and returns the configuration.
func (e *Evaluator) evaluateApplyConfigurationMutation(
	mutation admissionv1beta1.Mutation,
	vars map[string]any,
) (*unstructured.Unstructured, error) {
	if mutation.ApplyConfiguration == nil {
		//nolint:nilnil // No configuration to apply, no error
		return nil, nil
	}

	// For ApplyConfiguration, we need the CEL value, not the unwrapped Go value
	patchResult, err := e.evaluateExpressionRaw(mutation.ApplyConfiguration.Expression, vars)
	if err != nil {
		return nil, fmt.Errorf("evaluate ApplyConfiguration expression: %w", err)
	}

	if patchResult == nil {
		//nolint:nilnil // result is nil, no error
		return nil, nil
	}

	// ApplyConfiguration returns an ObjectVal from CEL
	objVal, ok := patchResult.(*dynamic.ObjectVal)
	if !ok {
		return nil, fmt.Errorf("%w: %T", errApplyConfigNotObject, patchResult)
	}

	// Recursively convert all CEL values to native Go types
	convertedValue, ok := convertCELValue(objVal).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: %T", errConvertCELUnexpectedType, convertCELValue(objVal))
	}

	return &unstructured.Unstructured{Object: convertedValue}, nil
}

// Follows the Kubernetes pattern from k8s.io/apiserver/pkg/admission/plugin/policy/mutating/patch/json_patch.go.
func (e *Evaluator) applyJSONPatches(
	patches []any,
	object *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	if len(patches) == 0 {
		return object.DeepCopy(), nil
	}

	result := jsonpatch.Patch{}

	for _, p := range patches {
		iter, skip := listerFromPatch(p)
		if skip {
			continue
		}

		if err := appendPatchOperations(iter.Iterator(), &result); err != nil {
			return nil, err
		}
	}

	if len(result) == 0 {
		return object.DeepCopy(), nil
	}

	return applyPatchOperations(result, object)
}

func listerFromPatch(p any) (traits.Lister, bool) {
	if iter, ok := p.(traits.Lister); ok {
		return iter, false
	}

	if celArr, ok := p.([]ref.Val); ok {
		return &refValList{values: celArr}, false
	}

	return nil, true
}

func appendPatchOperations(iter traits.Iterator, result *jsonpatch.Patch) error {
	for iter.HasNext() == types.True {
		op, err := buildJSONPatchOperation(iter.Next())
		if err != nil {
			return err
		}

		*result = append(*result, op)
	}

	return nil
}

func buildJSONPatchOperation(value ref.Val) (jsonpatch.Operation, error) {
	patchObj, err := value.ConvertToNative(reflect.TypeOf(&mutation.JSONPatchVal{}))
	if err != nil {
		return nil, fmt.Errorf("convert patch element: %w", err)
	}

	op, ok := patchObj.(*mutation.JSONPatchVal)
	if !ok {
		return nil, fmt.Errorf("%w: %T", errUnexpectedPatchType, patchObj)
	}

	resultOp := jsonpatch.Operation{}
	resultOp["op"] = ptr.To(json.RawMessage(strconv.Quote(op.Op)))
	resultOp["path"] = ptr.To(json.RawMessage(strconv.Quote(op.Path)))

	if len(op.From) > 0 {
		resultOp["from"] = ptr.To(json.RawMessage(strconv.Quote(op.From)))
	}

	if op.Val != nil {
		converted, err := op.Val.ConvertToNative(reflect.TypeOf(&structpb.Value{}))
		if err != nil {
			return nil, fmt.Errorf("convert patch value: %w", err)
		}

		b, err := json.Marshal(converted)
		if err != nil {
			return nil, fmt.Errorf("marshal patch value: %w", err)
		}

		resultOp["value"] = ptr.To[json.RawMessage](b)
	}

	return resultOp, nil
}

func applyPatchOperations(result jsonpatch.Patch, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	objectJSON, err := json.Marshal(object.Object)
	if err != nil {
		return nil, fmt.Errorf("marshal object: %w", err)
	}

	patchedJSON, err := result.Apply(objectJSON)
	if err != nil {
		return nil, fmt.Errorf("apply patch: %w", err)
	}

	patchedObject := &unstructured.Unstructured{}
	if err := json.Unmarshal(patchedJSON, &patchedObject.Object); err != nil {
		return nil, fmt.Errorf("unmarshal patched object: %w", err)
	}

	return patchedObject, nil
}

// applyApplyConfigurations applies a collection of ApplyConfiguration configs to an object using strategic merge.
func (e *Evaluator) applyApplyConfigurations(
	configs []*unstructured.Unstructured,
	object *unstructured.Unstructured,
) *unstructured.Unstructured {
	if len(configs) == 0 {
		return object
	}

	// Start with a copy of the object
	result := object.DeepCopy()

	// Apply each configuration using strategic merge
	for _, config := range configs {
		// This is a simplified version - real implementation would use structured-merge-diff
		mergeObjects(result.Object, config.Object)
	}

	return result
}

// mergeObjects performs a strategic merge of src into dst.
// This is a simplified merge that recursively merges nested maps,
// replaces slices, and overwrites primitive values.
func mergeObjects(dst, src map[string]any) {
	for key, srcVal := range src {
		if dstVal, exists := dst[key]; exists {
			// If both are maps, merge recursively
			if dstMap, dstOk := dstVal.(map[string]any); dstOk {
				if srcMap, srcOk := srcVal.(map[string]any); srcOk {
					mergeObjects(dstMap, srcMap)

					continue
				}
			}
		}
		// For everything else (slices, primitives, or type mismatches), overwrite
		dst[key] = srcVal
	}
}

// convertCELValue recursively converts CEL ref.Val to native Go values.
// This ensures that nested maps and slices contain plain Go values, not CEL types.
//
//nolint:cyclop // Conversion needs multiple type-specific branches
func convertCELValue(val any) any {
	// If it's a CEL value, call Value() to get native value
	if celVal, ok := val.(ref.Val); ok {
		val = celVal.Value()
	}

	// Recursively convert maps
	if m, ok := val.(map[ref.Val]ref.Val); ok {
		result := make(map[string]any, len(m))

		for k, v := range m {
			keyVal := k.Value()
			if keyStr, ok := keyVal.(string); ok {
				result[keyStr] = convertCELValue(v)
			}
		}

		return result
	}

	// Recursively convert slices
	if s, ok := val.([]ref.Val); ok {
		result := make([]any, len(s))
		for i, item := range s {
			result[i] = convertCELValue(item)
		}

		return result
	}

	// For plain maps, convert values recursively
	if m, ok := val.(map[string]any); ok {
		result := make(map[string]any, len(m))
		for k, v := range m {
			result[k] = convertCELValue(v)
		}

		return result
	}

	// For plain slices, convert items recursively
	if s, ok := val.([]any); ok {
		result := make([]any, len(s))
		for i, item := range s {
			result[i] = convertCELValue(item)
		}

		return result
	}

	return val
}

// refValList is a simple wrapper to make []ref.Val implement traits.Lister.
type refValList struct {
	values []ref.Val
}

func (l *refValList) Add(value ref.Val) ref.Val {
	l.values = append(l.values, value)

	return l
}

func (l *refValList) Get(index ref.Val) ref.Val {
	if i, ok := index.Value().(int64); ok {
		if i >= 0 && i < int64(len(l.values)) {
			return l.values[i]
		}
	}

	return types.NewErr("index out of bounds")
}

func (l *refValList) Size() ref.Val {
	return types.Int(len(l.values))
}

func (l *refValList) Iterator() traits.Iterator {
	return &refValIterator{values: l.values, index: 0}
}

func (l *refValList) Contains(value ref.Val) ref.Val {
	for _, v := range l.values {
		if v.Equal(value) == types.True {
			return types.True
		}
	}

	return types.False
}

func (l *refValList) ConvertToNative(_ reflect.Type) (any, error) {
	return nil, errConversionNotSupported
}

func (l *refValList) ConvertToType(_ ref.Type) ref.Val {
	return types.NewErr("conversion not supported")
}

func (l *refValList) Equal(_ ref.Val) ref.Val {
	return types.False
}

func (l *refValList) Type() ref.Type {
	return types.ListType
}

func (l *refValList) Value() any {
	return l.values
}

// refValIterator implements traits.Iterator for []ref.Val.
type refValIterator struct {
	values []ref.Val
	index  int
}

func (it *refValIterator) HasNext() ref.Val {
	if it.index < len(it.values) {
		return types.True
	}

	return types.False
}

func (it *refValIterator) Next() ref.Val {
	if it.index < len(it.values) {
		v := it.values[it.index]
		it.index++

		return v
	}

	return types.NewErr("iterator exhausted")
}

func (it *refValIterator) ConvertToNative(_ reflect.Type) (any, error) {
	return nil, errConversionNotSupported
}

func (it *refValIterator) ConvertToType(_ ref.Type) ref.Val {
	return types.NewErr("conversion not supported")
}

func (it *refValIterator) Equal(_ ref.Val) ref.Val {
	return types.False
}

func (it *refValIterator) Type() ref.Type {
	return types.IteratorType
}

func (it *refValIterator) Value() any {
	return it
}
