package evaluator

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	celgo "github.com/google/cel-go/cel"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validate/content"
	genericvalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	plugincel "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/mutating/patch"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/matchconditions"
	apiservercel "k8s.io/apiserver/pkg/cel"
	"k8s.io/apiserver/pkg/cel/environment"
)

// This file ports the API server's defaulting and create-time validation of
// admission policies and bindings (k8s.io/kubernetes pkg/apis/admissionregistration
// v1/defaults.go and validation/validation.go), which kat cannot import. Field
// paths and messages are kept identical so kat rejects exactly what the API
// server rejects, with the same error.

const (
	admissionRegistrationGroup = "admissionregistration.k8s.io"

	maxAuditAnnotations = 20
	// maxAuditAnnotationValueExpressionLength is the server's 5kb limit.
	maxAuditAnnotationValueExpressionLength = 5 * 1024
	maxMatchConditions                      = 64
)

//nolint:gochecknoglobals // Fixed sets from the API server's validation.
var (
	supportedFailurePolicies = sets.New(string(admissionregv1.Ignore), string(admissionregv1.Fail))
	supportedMatchPolicies   = sets.New(string(admissionregv1.Exact), string(admissionregv1.Equivalent))
	supportedOperations      = sets.New(
		string(admissionregv1.OperationAll), string(admissionregv1.Create), string(admissionregv1.Update),
		string(admissionregv1.Delete), string(admissionregv1.Connect),
	)
	// A MutatingAdmissionPolicy cannot match DELETE.
	supportedMutatingOperations = sets.New(
		string(admissionregv1.OperationAll), string(admissionregv1.Create), string(admissionregv1.Update),
		string(admissionregv1.Connect),
	)
	supportedReinvocationPolicies = sets.New(
		string(admissionregv1.NeverReinvocationPolicy), string(admissionregv1.IfNeededReinvocationPolicy),
	)
	supportedValidationPolicyReasons = sets.New(
		string(metav1.StatusReasonForbidden), string(metav1.StatusReasonInvalid),
		string(metav1.StatusReasonRequestEntityTooLarge),
	)
	supportedPatchTypes     = sets.New(string(admissionregv1.PatchTypeApplyConfiguration), string(admissionregv1.PatchTypeJSONPatch))
	supportedValidationActs = sets.New(string(admissionregv1.Deny), string(admissionregv1.Warn), string(admissionregv1.Audit))
	validScopes             = sets.New(string(admissionregv1.ClusterScope), string(admissionregv1.NamespacedScope), string(admissionregv1.AllScopes))
	newlineMatcher          = regexp.MustCompile(`[\n\r]+`)
	celIdentRegex           = regexp.MustCompile("^[_a-zA-Z][_a-zA-Z0-9]*$")
	celReservedIdentifiers  = sets.New("true", "false", "null", "in", "as", "break", "const", "continue", "else",
		"for", "function", "if", "import", "let", "loop", "package", "namespace", "return", "var", "void", "while")
)

// InvalidPolicyError is the API server's rejection of a policy or binding at
// creation, as the error `kubectl apply` reports.
type InvalidPolicyError struct {
	Kind   string
	Name   string
	Errors field.ErrorList
}

func (e *InvalidPolicyError) Error() string {
	return apierrors.NewInvalid(schema.GroupKind{Group: admissionRegistrationGroup, Kind: e.Kind}, e.Name, e.Errors).Error()
}

func invalid(kind, name string, errs field.ErrorList) error {
	if len(errs) == 0 {
		return nil
	}

	return &InvalidPolicyError{Kind: kind, Name: name, Errors: errs}
}

// defaultValidatingPolicy applies the API server's defaults in place.
func defaultValidatingPolicy(p *admissionregv1.ValidatingAdmissionPolicy) {
	if p.Spec.FailurePolicy == nil {
		p.Spec.FailurePolicy = new(admissionregv1.Fail)
	}

	defaultMatchResources(p.Spec.MatchConstraints)
}

func defaultMutatingPolicy(p *admissionregv1.MutatingAdmissionPolicy) {
	if p.Spec.FailurePolicy == nil {
		p.Spec.FailurePolicy = new(admissionregv1.Fail)
	}

	defaultMatchResources(p.Spec.MatchConstraints)
}

func defaultMatchResources(mr *admissionregv1.MatchResources) {
	if mr == nil {
		return
	}

	if mr.MatchPolicy == nil {
		mr.MatchPolicy = new(admissionregv1.Equivalent)
	}

	if mr.NamespaceSelector == nil {
		mr.NamespaceSelector = &metav1.LabelSelector{}
	}

	if mr.ObjectSelector == nil {
		mr.ObjectSelector = &metav1.LabelSelector{}
	}

	for _, rules := range [][]admissionregv1.NamedRuleWithOperations{mr.ResourceRules, mr.ExcludeResourceRules} {
		for i := range rules {
			if rules[i].Scope == nil {
				rules[i].Scope = new(admissionregv1.AllScopes)
			}
		}
	}
}

// validateValidatingPolicy is the server's ValidateValidatingAdmissionPolicy.
func (e *Evaluator) validateValidatingPolicy(p *admissionregv1.ValidatingAdmissionPolicy) error {
	errs := genericvalidation.ValidateObjectMeta(&p.ObjectMeta, false, genericvalidation.NameIsDNSSubdomain, field.NewPath("metadata"))
	errs = append(errs, e.validateValidatingPolicySpec(p.ObjectMeta, &p.Spec, field.NewPath("spec"))...)

	return invalid("ValidatingAdmissionPolicy", p.Name, errs)
}

//nolint:cyclop,funlen // Mirrors the server's validation function.
func (e *Evaluator) validateValidatingPolicySpec(
	meta metav1.ObjectMeta,
	spec *admissionregv1.ValidatingAdmissionPolicySpec,
	fldPath *field.Path,
) field.ErrorList {
	var errs field.ErrorList

	// The composited compiler is stateful, so each policy gets its own.
	var compiler plugincel.Compiler

	getCompiler := func() plugincel.Compiler {
		if compiler == nil {
			compiler = e.policyCompiler(len(spec.Variables) > 0)
		}

		return compiler
	}

	errs = append(errs, validateFailurePolicy(spec.FailurePolicy, fldPath.Child("failurePolicy"))...)

	allowParamsInMatchConditions := spec.ParamKind != nil
	if spec.ParamKind != nil {
		errs = append(errs, validateParamKind(*spec.ParamKind, fldPath.Child("paramKind"))...)
	}

	errs = append(errs, validateMatchConstraints(spec.MatchConstraints, nil, fldPath.Child("matchConstraints"))...)
	errs = append(errs, e.validateMatchConditions(spec.MatchConditions, allowParamsInMatchConditions, fldPath.Child("matchConditions"))...)

	for i := range spec.Variables {
		errs = append(errs, validateVariable(getCompiler(), &spec.Variables[i], spec.ParamKind, fldPath.Child("variables").Index(i))...)
	}

	if len(spec.Validations) == 0 && len(spec.AuditAnnotations) == 0 {
		errs = append(errs,
			field.Required(fldPath.Child("validations"), "validations or auditAnnotations must contain at least one item"),
			field.Required(fldPath.Child("auditAnnotations"), "validations or auditAnnotations must contain at least one item"),
		)

		return errs
	}

	for i := range spec.Validations {
		errs = append(errs, validateValidation(getCompiler(), &spec.Validations[i], spec.ParamKind, allowParamsInMatchConditions, fldPath.Child("validations").Index(i))...)
	}

	if spec.AuditAnnotations != nil {
		if len(spec.AuditAnnotations) > maxAuditAnnotations {
			errs = append(errs, field.Invalid(fldPath.Child("auditAnnotations"), spec.AuditAnnotations,
				fmt.Sprintf("must not have more than %d auditAnnotations", maxAuditAnnotations)))
		}

		keys := sets.New[string]()

		for i := range spec.AuditAnnotations {
			a := &spec.AuditAnnotations[i]
			errs = append(errs, validateAuditAnnotation(getCompiler(), meta, a, spec.ParamKind, fldPath.Child("auditAnnotations").Index(i))...)

			if keys.Has(a.Key) {
				errs = append(errs, field.Duplicate(fldPath.Child("auditAnnotations").Index(i).Child("key"), a.Key))
			}

			keys.Insert(a.Key)
		}
	}

	return errs
}

// validateMutatingPolicy is the server's ValidateMutatingAdmissionPolicy.
func (e *Evaluator) validateMutatingPolicy(p *admissionregv1.MutatingAdmissionPolicy) error {
	errs := genericvalidation.ValidateObjectMeta(&p.ObjectMeta, false, genericvalidation.NameIsDNSSubdomain, field.NewPath("metadata"))
	errs = append(errs, e.validateMutatingPolicySpec(&p.Spec, field.NewPath("spec"))...)

	return invalid("MutatingAdmissionPolicy", p.Name, errs)
}

func (e *Evaluator) validateMutatingPolicySpec(spec *admissionregv1.MutatingAdmissionPolicySpec, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	compiler := e.policyCompiler(true)

	errs = append(errs, validateFailurePolicy(spec.FailurePolicy, fldPath.Child("failurePolicy"))...)

	if spec.ParamKind != nil {
		errs = append(errs, validateParamKind(*spec.ParamKind, fldPath.Child("paramKind"))...)
	}

	errs = append(errs, validateMatchConstraints(spec.MatchConstraints, supportedMutatingOperations, fldPath.Child("matchConstraints"))...)
	errs = append(errs, e.validateMatchConditions(spec.MatchConditions, spec.ParamKind != nil, fldPath.Child("matchConditions"))...)

	for i := range spec.Variables {
		errs = append(errs, validateVariable(compiler, &spec.Variables[i], spec.ParamKind, fldPath.Child("variables").Index(i))...)
	}

	if len(spec.Mutations) == 0 {
		errs = append(errs, field.Required(fldPath.Child("mutations"), "mutations must contain at least one item"))
	}

	for i := range spec.Mutations {
		errs = append(errs, validateMutation(compiler, &spec.Mutations[i], spec.ParamKind, fldPath.Child("mutations").Index(i))...)
	}

	switch {
	case spec.ReinvocationPolicy == "":
		errs = append(errs, field.Required(fldPath.Child("reinvocationPolicy"), ""))
	case !supportedReinvocationPolicies.Has(string(spec.ReinvocationPolicy)):
		errs = append(errs, field.NotSupported(fldPath.Child("reinvocationPolicy"), spec.ReinvocationPolicy, sets.List(supportedReinvocationPolicies)))
	}

	return errs
}

// validateValidatingBinding is the server's ValidateValidatingAdmissionPolicyBinding.
func validateValidatingBinding(b *admissionregv1.ValidatingAdmissionPolicyBinding) error {
	fldPath := field.NewPath("spec")
	errs := genericvalidation.ValidateObjectMeta(&b.ObjectMeta, false, genericvalidation.NameIsDNSSubdomain, field.NewPath("metadata"))
	errs = append(errs, validatePolicyName(b.Spec.PolicyName, fldPath.Child("policyName"))...)
	errs = append(errs, validateParamRef(b.Spec.ParamRef, fldPath.Child("paramRef"))...)
	errs = append(errs, validateMatchResources(b.Spec.MatchResources, fldPath.Child("matchResources"))...)
	errs = append(errs, validateValidationActions(b.Spec.ValidationActions, fldPath.Child("validationActions"))...)

	return invalid("ValidatingAdmissionPolicyBinding", b.Name, errs)
}

// validateMutatingBinding is the server's ValidateMutatingAdmissionPolicyBinding.
func validateMutatingBinding(b *admissionregv1.MutatingAdmissionPolicyBinding) error {
	fldPath := field.NewPath("spec")
	errs := genericvalidation.ValidateObjectMeta(&b.ObjectMeta, false, genericvalidation.NameIsDNSSubdomain, field.NewPath("metadata"))
	errs = append(errs, validatePolicyName(b.Spec.PolicyName, fldPath.Child("policyName"))...)
	errs = append(errs, validateParamRef(b.Spec.ParamRef, fldPath.Child("paramRef"))...)
	errs = append(errs, validateMatchResources(b.Spec.MatchResources, fldPath.Child("matchResources"))...)

	if b.Spec.MatchResources != nil {
		errs = append(errs, validateOperations(b.Spec.MatchResources.ResourceRules, supportedMutatingOperations, fldPath.Child("matchResources", "resourceRules", "operations"))...)
	}

	return invalid("MutatingAdmissionPolicyBinding", b.Name, errs)
}

func validatePolicyName(name string, fldPath *field.Path) field.ErrorList {
	if name == "" {
		return field.ErrorList{field.Required(fldPath, "")}
	}

	var errs field.ErrorList
	for _, msg := range genericvalidation.NameIsDNSSubdomain(name, false) {
		errs = append(errs, field.Invalid(fldPath, name, msg))
	}

	return errs
}

func validateFailurePolicy(fp *admissionregv1.FailurePolicyType, fldPath *field.Path) field.ErrorList {
	switch {
	case fp == nil:
		return field.ErrorList{field.Required(fldPath, "")}
	case !supportedFailurePolicies.Has(string(*fp)):
		return field.ErrorList{field.NotSupported(fldPath, *fp, sets.List(supportedFailurePolicies))}
	}

	return nil
}

// validateMatchConstraints validates a policy's required matchConstraints,
// which need at least one resourceRule to provide type information.
func validateMatchConstraints(mc *admissionregv1.MatchResources, operations sets.Set[string], fldPath *field.Path) field.ErrorList {
	if mc == nil {
		return field.ErrorList{field.Required(fldPath, "")}
	}

	errs := validateMatchResources(mc, fldPath)
	if len(mc.ResourceRules) == 0 {
		errs = append(errs, field.Required(fldPath.Child("resourceRules"), ""))
	}

	if operations != nil {
		errs = append(errs, validateOperations(mc.ResourceRules, operations, fldPath.Child("resourceRules", "operations"))...)
	}

	return errs
}

func validateOperations(rules []admissionregv1.NamedRuleWithOperations, operations sets.Set[string], fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	for _, rule := range rules {
		for _, op := range rule.Operations {
			if !operations.Has(string(op)) {
				errs = append(errs, field.NotSupported(fldPath, op, sets.List(operations)))
			}
		}
	}

	return errs
}

func validateParamKind(gvk admissionregv1.ParamKind, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	switch gv, err := parseGroupVersion(gvk.APIVersion); {
	case gvk.APIVersion == "":
		errs = append(errs, field.Required(fldPath.Child("apiVersion"), ""))
	case err != nil:
		errs = append(errs, field.Invalid(fldPath.Child("apiVersion"), gvk.APIVersion, err.Error()))
	default:
		if gv.Group != "" {
			if msgs := utilvalidation.IsDNS1123Subdomain(gv.Group); len(msgs) > 0 {
				errs = append(errs, field.Invalid(fldPath.Child("apiVersion"), gv.Group, strings.Join(msgs, ",")))
			}
		}

		if gv.Version == "" {
			errs = append(errs, field.Invalid(fldPath.Child("apiVersion"), gvk.APIVersion, "version must be specified"))
		} else if msgs := utilvalidation.IsDNS1035Label(gv.Version); len(msgs) > 0 {
			errs = append(errs, field.Invalid(fldPath.Child("apiVersion"), gv.Version, strings.Join(msgs, ",")))
		}
	}

	if gvk.Kind == "" {
		errs = append(errs, field.Required(fldPath.Child("kind"), ""))
	} else if msgs := utilvalidation.IsDNS1035Label(strings.ToLower(gvk.Kind)); len(msgs) > 0 {
		errs = append(errs, field.Invalid(fldPath.Child("kind"), gvk.Kind, "may have mixed case, but should otherwise match: "+strings.Join(msgs, ",")))
	}

	return errs
}

var errUnexpectedGroupVersion = errors.New("unexpected GroupVersion string")

func parseGroupVersion(gv string) (schema.GroupVersion, error) {
	if gv == "" || gv == "/" {
		return schema.GroupVersion{}, nil
	}

	switch strings.Count(gv, "/") {
	case 0:
		return schema.GroupVersion{Version: gv}, nil
	case 1:
		group, version, _ := strings.Cut(gv, "/")

		return schema.GroupVersion{Group: group, Version: version}, nil
	default:
		return schema.GroupVersion{}, fmt.Errorf("%w: %v", errUnexpectedGroupVersion, gv)
	}
}

func validateMatchResources(mc *admissionregv1.MatchResources, fldPath *field.Path) field.ErrorList {
	if mc == nil {
		return nil
	}

	var errs field.ErrorList

	switch {
	case mc.MatchPolicy == nil:
		errs = append(errs, field.Required(fldPath.Child("matchPolicy"), ""))
	case !supportedMatchPolicies.Has(string(*mc.MatchPolicy)):
		errs = append(errs, field.NotSupported(fldPath.Child("matchPolicy"), *mc.MatchPolicy, sets.List(supportedMatchPolicies)))
	}

	errs = append(errs, validateSelector(mc.NamespaceSelector, fldPath.Child("namespaceSelector"))...)
	errs = append(errs, validateSelector(mc.ObjectSelector, fldPath.Child("objectSelector"))...)

	for i := range mc.ResourceRules {
		errs = append(errs, validateNamedRule(&mc.ResourceRules[i], fldPath.Child("resourceRules").Index(i))...)
	}

	for i := range mc.ExcludeResourceRules {
		errs = append(errs, validateNamedRule(&mc.ExcludeResourceRules[i], fldPath.Child("excludeResourceRules").Index(i))...)
	}

	return errs
}

func validateSelector(selector *metav1.LabelSelector, fldPath *field.Path) field.ErrorList {
	if selector == nil {
		return field.ErrorList{field.Required(fldPath, "")}
	}

	return metav1validation.ValidateLabelSelector(selector, metav1validation.LabelSelectorValidationOptions{}, fldPath)
}

func validateNamedRule(n *admissionregv1.NamedRuleWithOperations, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	names := sets.New[string]()

	for i, name := range n.ResourceNames {
		for _, msg := range content.IsPathSegmentName(name) {
			errs = append(errs, field.Invalid(fldPath.Child("resourceNames").Index(i), name, msg))
		}

		if names.Has(name) {
			errs = append(errs, field.Duplicate(fldPath.Child("resourceNames").Index(i), name))
		}

		names.Insert(name)
	}

	return append(errs, validateRuleWithOperations(&n.RuleWithOperations, fldPath)...)
}

func validateRuleWithOperations(r *admissionregv1.RuleWithOperations, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	if len(r.Operations) == 0 {
		errs = append(errs, field.Required(fldPath.Child("operations"), ""))
	}

	if len(r.Operations) > 1 && hasWildcard(r.Operations) {
		errs = append(errs, field.Invalid(fldPath.Child("operations"), r.Operations, "if '*' is present, must not specify other operations"))
	}

	for i, op := range r.Operations {
		if !supportedOperations.Has(string(op)) {
			errs = append(errs, field.NotSupported(fldPath.Child("operations").Index(i), op, sets.List(supportedOperations)))
		}
	}

	return append(errs, validateRule(&r.Rule, fldPath)...)
}

//nolint:cyclop // Mirrors the server's validation function.
func validateRule(rule *admissionregv1.Rule, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	if len(rule.APIGroups) == 0 {
		errs = append(errs, field.Required(fldPath.Child("apiGroups"), ""))
	}

	if len(rule.APIGroups) > 1 && hasWildcard(rule.APIGroups) {
		errs = append(errs, field.Invalid(fldPath.Child("apiGroups"), rule.APIGroups, "if '*' is present, must not specify other API groups"))
	}

	if len(rule.APIVersions) == 0 {
		errs = append(errs, field.Required(fldPath.Child("apiVersions"), ""))
	}

	if len(rule.APIVersions) > 1 && hasWildcard(rule.APIVersions) {
		errs = append(errs, field.Invalid(fldPath.Child("apiVersions"), rule.APIVersions, "if '*' is present, must not specify other API versions"))
	}

	for i, v := range rule.APIVersions {
		if v == "" {
			errs = append(errs, field.Required(fldPath.Child("apiVersions").Index(i), ""))
		}
	}

	errs = append(errs, validateResources(rule.Resources, fldPath.Child("resources"))...)

	if rule.Scope != nil && !validScopes.Has(string(*rule.Scope)) {
		errs = append(errs, field.NotSupported(fldPath.Child("scope"), *rule.Scope, sets.List(validScopes)))
	}

	return errs
}

//nolint:cyclop // Mirrors the server's validation function.
func validateResources(resources []string, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	if len(resources) == 0 {
		errs = append(errs, field.Required(fldPath, ""))
	}

	withWildcardSubresource := sets.New[string]() // x/*
	withWildcardResource := sets.New[string]()    // */x
	hasDoubleWildcard, hasSingleWildcard, hasResourceWithoutSubresource := false, false, false

	for i, resSub := range resources {
		if resSub == "" {
			errs = append(errs, field.Required(fldPath.Index(i), ""))

			continue
		}

		hasDoubleWildcard = hasDoubleWildcard || resSub == "*/*"
		hasSingleWildcard = hasSingleWildcard || resSub == "*"

		res, sub, found := strings.Cut(resSub, "/")
		if !found {
			hasResourceWithoutSubresource = resSub != "*"

			continue
		}

		if withWildcardSubresource.Has(res) {
			errs = append(errs, field.Invalid(fldPath.Index(i), resSub, fmt.Sprintf("if '%s/*' is present, must not specify %s", res, resSub)))
		}

		if withWildcardResource.Has(sub) {
			errs = append(errs, field.Invalid(fldPath.Index(i), resSub, fmt.Sprintf("if '*/%s' is present, must not specify %s", sub, resSub)))
		}

		if sub == "*" {
			withWildcardSubresource.Insert(res)
		}

		if res == "*" {
			withWildcardResource.Insert(sub)
		}
	}

	if len(resources) > 1 && hasDoubleWildcard {
		errs = append(errs, field.Invalid(fldPath, resources, "if '*/*' is present, must not specify other resources"))
	}

	if hasSingleWildcard && hasResourceWithoutSubresource {
		errs = append(errs, field.Invalid(fldPath, resources, "if '*' is present, must not specify other resources without subresources"))
	}

	return errs
}

func hasWildcard[T ~string](values []T) bool {
	return slices.Contains(values, "*")
}

func validateValidationActions(actions []admissionregv1.ValidationAction, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	seen := sets.New[string]()

	for i, action := range actions {
		if !supportedValidationActs.Has(string(action)) {
			errs = append(errs, field.NotSupported(fldPath.Index(i), action, sets.List(supportedValidationActs)))
		}

		if seen.Has(string(action)) {
			errs = append(errs, field.Duplicate(fldPath.Index(i), action))
		}

		seen.Insert(string(action))
	}

	if seen.Has(string(admissionregv1.Deny)) && seen.Has(string(admissionregv1.Warn)) {
		errs = append(errs, field.Invalid(fldPath, actions,
			"must not contain both Deny and Warn (repeating the same validation failure information in the API response and headers serves no purpose)"))
	}

	if seen.Len() == 0 {
		errs = append(errs, field.Required(fldPath, "at least one validation action is required"))
	}

	return errs
}

//nolint:cyclop // Mirrors the server's validation function.
func validateParamRef(pr *admissionregv1.ParamRef, fldPath *field.Path) field.ErrorList {
	if pr == nil {
		return nil
	}

	var errs field.ErrorList

	if pr.Name != "" {
		for _, msg := range content.IsPathSegmentName(pr.Name) {
			errs = append(errs, field.Invalid(fldPath.Child("name"), pr.Name, msg))
		}

		if pr.Selector != nil {
			errs = append(errs, field.Forbidden(fldPath.Child("name"), `name and selector are mutually exclusive`))
		}
	}

	if pr.Selector != nil {
		errs = append(errs, metav1validation.ValidateLabelSelector(pr.Selector, metav1validation.LabelSelectorValidationOptions{}, fldPath.Child("selector"))...)

		if pr.Name != "" {
			errs = append(errs, field.Forbidden(fldPath.Child("selector"), `name and selector are mutually exclusive`))
		}
	}

	if pr.Name == "" && pr.Selector == nil {
		errs = append(errs, field.Required(fldPath, `one of name or selector must be specified`))
	}

	switch action := pr.ParameterNotFoundAction; {
	case action == nil || *action == "":
		errs = append(errs, field.Required(fldPath.Child("parameterNotFoundAction"), ""))
	case *action != admissionregv1.DenyAction && *action != admissionregv1.AllowAction:
		errs = append(errs, field.NotSupported(fldPath.Child("parameterNotFoundAction"), action,
			[]string{string(admissionregv1.DenyAction), string(admissionregv1.AllowAction)}))
	}

	return errs
}

func (e *Evaluator) validateMatchConditions(conditions []admissionregv1.MatchCondition, allowParams bool, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	if len(conditions) > maxMatchConditions {
		errs = append(errs, field.TooMany(fldPath, len(conditions), maxMatchConditions))
	}

	names := sets.New[string]()

	for i, mc := range conditions {
		p := fldPath.Index(i)

		if expression := strings.TrimSpace(mc.Expression); expression == "" {
			errs = append(errs, field.Required(p.Child("expression"), ""))
		} else {
			condition := &matchconditions.MatchCondition{Expression: expression}
			errs = append(errs, validateCELExpression(e.statelessCompiler, condition,
				plugincel.OptionalVariableDeclarations{HasParams: allowParams, HasAuthorizer: true}, p.Child("expression"))...)
		}

		if mc.Name == "" {
			errs = append(errs, field.Required(p.Child("name"), ""))

			continue
		}

		errs = append(errs, validateQualifiedName(mc.Name, p.Child("name"))...)

		if names.Has(mc.Name) {
			errs = append(errs, field.Duplicate(p.Child("name"), mc.Name))
		}

		names.Insert(mc.Name)
	}

	return errs
}

func validateVariable(compiler plugincel.Compiler, v *admissionregv1.Variable, paramKind *admissionregv1.ParamKind, fldPath *field.Path) field.ErrorList {
	var errs field.ErrorList

	if strings.TrimSpace(v.Name) == "" {
		errs = append(errs, field.Required(fldPath.Child("name"), "name is not specified"))
	} else if !celIdentRegex.MatchString(v.Name) || celReservedIdentifiers.Has(v.Name) {
		errs = append(errs, field.Invalid(fldPath.Child("name"), v.Name, "must be a valid CEL identifier"))
	}

	if strings.TrimSpace(v.Expression) == "" {
		return append(errs, field.Required(fldPath.Child("expression"), "expression is not specified"))
	}

	composited, ok := compiler.(*plugincel.CompositedCompiler)
	if !ok {
		return append(errs, field.InternalError(fldPath, errVariableComposition))
	}

	variable := &namedExpression{name: v.Name, expression: v.Expression}
	result := composited.CompileAndStoreVariable(variable,
		plugincel.OptionalVariableDeclarations{HasParams: paramKind != nil, HasAuthorizer: true}, environment.NewExpressions)

	if result.Error != nil {
		errs = append(errs, celValidationError(fldPath.Child("expression"), variable, result.Error))
	}

	return errs
}

var errVariableComposition = errors.New("variable composition is not allowed")

func validateValidation(
	compiler plugincel.Compiler,
	v *admissionregv1.Validation,
	paramKind *admissionregv1.ParamKind,
	allowParamsInMessage bool,
	fldPath *field.Path,
) field.ErrorList {
	var errs field.ErrorList

	trimmedMsg := strings.TrimSpace(v.Message)
	trimmedMessageExpression := strings.TrimSpace(v.MessageExpression)

	if strings.TrimSpace(v.Expression) == "" {
		errs = append(errs, field.Required(fldPath.Child("expression"), "expression is not specified"))
	} else {
		errs = append(errs, validateCELExpression(compiler, &typedExpression{expression: v.Expression, returnTypes: boolType},
			plugincel.OptionalVariableDeclarations{HasParams: paramKind != nil, HasAuthorizer: true}, fldPath.Child("expression"))...)
	}

	if v.MessageExpression != "" && trimmedMessageExpression == "" {
		errs = append(errs, field.Invalid(fldPath.Child("messageExpression"), v.MessageExpression, "must be non-empty if specified"))
	} else if trimmedMessageExpression != "" {
		// The untrimmed expression keeps the compiler's column numbers right.
		errs = append(errs, validateCELExpression(compiler, &typedExpression{expression: v.MessageExpression, returnTypes: stringType},
			plugincel.OptionalVariableDeclarations{HasParams: allowParamsInMessage}, fldPath.Child("messageExpression"))...)
	}

	if v.Message != "" && trimmedMsg == "" {
		errs = append(errs, field.Invalid(fldPath.Child("message"), v.Message, "must be non-empty if specified"))
	} else if newlineMatcher.MatchString(trimmedMsg) {
		errs = append(errs, field.Invalid(fldPath.Child("message"), v.Message, "must not contain line breaks"))
	}

	if v.Reason != nil && !supportedValidationPolicyReasons.Has(string(*v.Reason)) {
		errs = append(errs, field.NotSupported(fldPath.Child("reason"), *v.Reason, sets.List(supportedValidationPolicyReasons)))
	}

	return errs
}

func validateAuditAnnotation(
	compiler plugincel.Compiler,
	meta metav1.ObjectMeta,
	a *admissionregv1.AuditAnnotation,
	paramKind *admissionregv1.ParamKind,
	fldPath *field.Path,
) field.ErrorList {
	var errs field.ErrorList

	if meta.GetName() != "" {
		errs = append(errs, validateQualifiedName(meta.GetName()+"/"+a.Key, fldPath.Child("key"))...)
	} else {
		errs = append(errs, field.Invalid(fldPath.Child("key"), a.Key, "requires metadata.name be non-empty"))
	}

	trimmed := strings.TrimSpace(a.ValueExpression)

	switch {
	case trimmed == "":
		errs = append(errs, field.Required(fldPath.Child("valueExpression"), "valueExpression is not specified"))
	case len(trimmed) > maxAuditAnnotationValueExpressionLength:
		errs = append(errs, field.Required(fldPath.Child("valueExpression"),
			fmt.Sprintf("must not exceed %d bytes in length", maxAuditAnnotationValueExpressionLength)))
	default:
		result := compiler.CompileCELExpression(&typedExpression{expression: trimmed, returnTypes: stringOrNullType},
			plugincel.OptionalVariableDeclarations{HasParams: paramKind != nil, HasAuthorizer: true}, environment.NewExpressions)
		if result.Error != nil {
			// The server reports the expression as written, not trimmed.
			errs = append(errs, celValidationError(fldPath.Child("valueExpression"), &typedExpression{expression: a.ValueExpression}, result.Error))
		}
	}

	return errs
}

func validateMutation(compiler plugincel.Compiler, m *admissionregv1.Mutation, paramKind *admissionregv1.ParamKind, fldPath *field.Path) field.ErrorList {
	if m.PatchType == "" {
		return field.ErrorList{field.Required(fldPath.Child("patchType"), "")}
	}

	var errs field.ErrorList

	opts := plugincel.OptionalVariableDeclarations{HasParams: paramKind != nil, HasAuthorizer: true, HasPatchTypes: true}

	switch m.PatchType {
	case admissionregv1.PatchTypeJSONPatch:
		if m.JSONPatch == nil {
			errs = append(errs, field.Required(fldPath.Child("jsonPatch"), "must be specified when patchType is JSONPatch"))
		} else {
			errs = append(errs, validatePatchExpression(compiler, &patch.JSONPatchCondition{Expression: strings.TrimSpace(m.JSONPatch.Expression)}, opts, fldPath.Child("jsonPatch", "expression"))...)
		}

		if m.ApplyConfiguration != nil {
			errs = append(errs, field.Invalid(fldPath.Child("applyConfiguration"), "{applyConfiguration}", "must not be specified when patchType is JSONPatch"))
		}
	case admissionregv1.PatchTypeApplyConfiguration:
		if m.ApplyConfiguration == nil {
			errs = append(errs, field.Required(fldPath.Child("applyConfiguration"), "must be specified when patchType is ApplyConfiguration"))
		} else {
			errs = append(errs, validatePatchExpression(compiler, &patch.ApplyConfigurationCondition{Expression: strings.TrimSpace(m.ApplyConfiguration.Expression)}, opts, fldPath.Child("applyConfiguration", "expression"))...)
		}

		if m.JSONPatch != nil {
			errs = append(errs, field.Invalid(fldPath.Child("jsonPatch"), "{jsonPatch}", "must not be specified when patchType is ApplyConfiguration"))
		}
	default:
		errs = append(errs, field.NotSupported(fldPath.Child("patchType"), m.PatchType, sets.List(supportedPatchTypes)))
	}

	return errs
}

func validatePatchExpression(compiler plugincel.Compiler, accessor plugincel.ExpressionAccessor, opts plugincel.OptionalVariableDeclarations, fldPath *field.Path) field.ErrorList {
	if accessor.GetExpression() == "" {
		return field.ErrorList{field.Required(fldPath, "")}
	}

	return validateCELExpression(compiler, accessor, opts, fldPath)
}

// validateCELExpression compiles a new expression, as the server does when a
// policy is created, and reports why it does not compile.
func validateCELExpression(compiler plugincel.Compiler, accessor plugincel.ExpressionAccessor, opts plugincel.OptionalVariableDeclarations, fldPath *field.Path) field.ErrorList {
	result := compiler.CompileCELExpression(accessor, opts, environment.NewExpressions)
	if result.Error == nil {
		return nil
	}

	return field.ErrorList{celValidationError(fldPath, accessor, result.Error)}
}

func celValidationError(fldPath *field.Path, accessor plugincel.ExpressionAccessor, err *apiservercel.Error) *field.Error {
	switch err.Type {
	case apiservercel.ErrorTypeRequired:
		return field.Required(fldPath, err.Detail)
	case apiservercel.ErrorTypeInvalid:
		return field.Invalid(fldPath, accessor.GetExpression(), err.Detail)
	case apiservercel.ErrorTypeInternal:
		return field.InternalError(fldPath, err)
	}

	return field.InternalError(fldPath, fmt.Errorf("unsupported error type: %w", err))
}

func validateQualifiedName(value string, fldPath *field.Path) field.ErrorList {
	msgs := utilvalidation.IsQualifiedName(value)

	errs := make(field.ErrorList, 0, len(msgs))
	for _, msg := range msgs {
		errs = append(errs, field.Invalid(fldPath, value, msg))
	}

	return errs
}

//nolint:gochecknoglobals // Return types of the server's expression accessors.
var (
	boolType         = []*celgo.Type{celgo.BoolType}
	stringType       = []*celgo.Type{celgo.StringType}
	stringOrNullType = []*celgo.Type{celgo.StringType, celgo.NullType}
	anyType          = []*celgo.Type{celgo.AnyType, celgo.DynType}
)

// typedExpression is an expression and the types it must evaluate to, like
// the server's ValidationCondition, MessageExpressionCondition and
// AuditAnnotationCondition accessors.
type typedExpression struct {
	expression  string
	returnTypes []*celgo.Type
}

func (t *typedExpression) GetExpression() string      { return t.expression }
func (t *typedExpression) ReturnTypes() []*celgo.Type { return t.returnTypes }

// namedExpression is a policy variable.
type namedExpression struct {
	name, expression string
}

func (n *namedExpression) GetExpression() string      { return n.expression }
func (n *namedExpression) ReturnTypes() []*celgo.Type { return anyType }
func (n *namedExpression) GetName() string            { return n.name }
