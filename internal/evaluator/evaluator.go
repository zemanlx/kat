package evaluator

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/ext"
	"github.com/pmezard/go-difflib/difflib"
	"gopkg.in/yaml.v3"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	plugin "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	celcommon "k8s.io/apiserver/pkg/cel/common"
	"k8s.io/apiserver/pkg/cel/lazy"
	"k8s.io/apiserver/pkg/cel/library"
	"k8s.io/apiserver/pkg/cel/mutation"
	"k8s.io/apiserver/pkg/cel/mutation/dynamic"

	"github.com/zemanlx/kat/internal/ssa"
)

var (
	errMutatingRequiresObject   = errors.New("mutating policy requires object or oldObject")
	errUnsupportedPatchType     = errors.New("unsupported patch type")
	errValidationNonBoolean     = errors.New("validation expression returned non-boolean")
	errMatchConditionNonBoolean = errors.New("match condition returned non-boolean")
	errApplyConfigNotObject     = errors.New("apply configuration must return Object")
	errConvertCELUnexpectedType = errors.New("convertCELValue returned unexpected type")
	errUnexpectedPatchType      = errors.New("unexpected patch type")
	errJSONPatchNotList         = errors.New("JSONPatch expression should evaluate to a list")
	errNoPolicy                 = errors.New("no policy provided")
	errUndeclaredVariable       = errors.New("reference to an undeclared variable")
)

const diffContextLines = 3

// Evaluator evaluates admission policies using CEL expressions.
type Evaluator struct {
	env *cel.Env
	// matchEnv compiles matchConditions, which cannot reference variables.
	matchEnv *cel.Env
}

// New creates a new Evaluator with a CEL environment configured for Kubernetes admission policies.
func New() (*Evaluator, error) {
	// Build environment options with all Kubernetes CEL libraries
	envOpts := []cel.EnvOption{
		// Options of the API server's base environment
		// (k8s.io/apiserver v0.37 pkg/cel/environment/base.go).
		cel.HomogeneousAggregateLiterals(),
		cel.EagerlyValidateDeclarations(true),
		cel.DefaultUTCTimeZone(true),
		cel.CrossTypeNumericComparisons(true),
		cel.ASTValidators(
			cel.ValidateDurationLiterals(),
			cel.ValidateTimestampLiterals(),
			cel.ValidateRegexLiterals(),
			cel.ValidateHomogeneousAggregateLiterals(),
		),
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
		// CEL extensions registered by k8s.io/apiserver v0.37 environment/base.go
		// (plus Math/Encoders, which admission does not register but existing policies use).
		cel.OptionalTypes(), // 1.28: object.?field, .orValue()
		ext.Encoders(),
		ext.Lists(),
		ext.Math(),
		ext.Sets(), // 1.29
		ext.Strings(),
		ext.TwoVarComprehensions(), // 1.32: transformMap / transformMapEntry
		celcommon.ResolverEnvOption(&mutation.DynamicTypeResolver{}),
	}

	matchEnv, err := cel.NewEnv(envOpts...)
	if err != nil {
		return nil, fmt.Errorf("create CEL environment: %w", err)
	}

	env, err := matchEnv.Extend(cel.Variable(plugin.VariableVarName, cel.DynType))
	if err != nil {
		return nil, fmt.Errorf("create CEL environment: %w", err)
	}

	return &Evaluator{env: env, matchEnv: matchEnv}, nil
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
	mutatingPolicy *admissionregv1.MutatingAdmissionPolicy,
	mutatingBinding *admissionregv1.MutatingAdmissionPolicyBinding,
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

	// If the policy mutated the object but no .gold.yaml was provided, fail.
	if result.PatchedObject != nil && expected.Object == nil {
		result.Passed = false
		result.Message = "policy mutated the object but no .gold.yaml file was provided"

		return result
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
	mutatingPolicy *admissionregv1.MutatingAdmissionPolicy,
	mutatingBinding *admissionregv1.MutatingAdmissionPolicyBinding,
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
			expectedYAML = fmt.Appendf(nil, "%+v", expected.AuditAnnotations)
		}

		actualYAML, err := yaml.Marshal(actualFiltered)
		if err != nil {
			actualYAML = fmt.Appendf(nil, "%+v", actualFiltered)
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
			expectedYAML = fmt.Appendf(nil, "%+v", expected.Object.Object)
		}

		actualYAML, err := yaml.Marshal(actual.Object.Object)
		if err != nil {
			actualYAML = fmt.Appendf(nil, "%+v", actual.Object.Object)
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

// validationMessage returns a failed validation's message the way the API
// server picks it: a usable messageExpression result, else message, else the
// expression itself. A messageExpression that errors, is not a string, is
// blank or spans lines falls back to message.
func (e *Evaluator) validationMessage(validation *admissionregv1.Validation, vars map[string]any) string {
	if validation.MessageExpression != "" {
		result, err := e.evaluateExpression(validation.MessageExpression, vars)
		if msg, ok := result.(string); err == nil && ok {
			msg = strings.TrimSpace(msg)
			if msg != "" && !strings.Contains(msg, "\n") {
				return msg
			}
		}
	}

	if msg := strings.TrimSpace(validation.Message); msg != "" {
		return msg
	}

	return "failed expression: " + strings.TrimSpace(validation.Expression)
}

// failuresResult applies the binding's validationActions to the failure
// messages: Warn reports each of them as a warning, and Deny denies with the
// first. A missing binding or empty validationActions denies.
func failuresResult(
	failures []string,
	binding *admissionregv1.ValidatingAdmissionPolicyBinding,
	auditAnnotations map[string]string,
) *EvaluationResult {
	result := &EvaluationResult{Allowed: true, AuditAnnotations: auditAnnotations}
	if len(failures) == 0 {
		return result
	}

	actions := []admissionregv1.ValidationAction{admissionregv1.Deny}
	if binding != nil && len(binding.Spec.ValidationActions) > 0 {
		actions = binding.Spec.ValidationActions
	}

	if slices.Contains(actions, admissionregv1.Warn) {
		result.Warnings = failures
	}

	if slices.Contains(actions, admissionregv1.Deny) {
		result.Allowed = false
		result.Message = failures[0]
	}

	return result
}

// ignoresFailures reports whether the policy's failurePolicy is Ignore; the
// API server defaults it to Fail.
func ignoresFailures(failurePolicy *admissionregv1.FailurePolicyType) bool {
	return failurePolicy != nil && *failurePolicy == admissionregv1.Ignore
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
	policy *admissionregv1.MutatingAdmissionPolicy,
	binding *admissionregv1.MutatingAdmissionPolicyBinding,
	request *admissionv1.AdmissionRequest,
	object *unstructured.Unstructured,
	oldObject *unstructured.Unstructured,
	params *unstructured.Unstructured,
	namespaceObj *unstructured.Unstructured,
	authorizer authorizer.Authorizer,
	userInfo user.Info,
) (*EvaluationResult, error) {
	if result, err := e.preflight(mutatingShape(policy, binding), request, object, oldObject, params, namespaceObj); result != nil || err != nil {
		return result, err
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

	e.bindVariables(policy.Spec.Variables, vars)

	matched, failure, err := e.evaluateMatchConditions(policy.Spec.MatchConditions, vars, policy.Spec.FailurePolicy)
	if err != nil {
		return nil, fmt.Errorf("evaluate match conditions: %w", err)
	}

	if failure != "" {
		return &EvaluationResult{Allowed: false, Message: failure}, nil
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

// policyShape is what the checks made before evaluating any expression need
// to know about a policy and its binding.
type policyShape struct {
	constraints      *admissionregv1.MatchResources
	bindingResources *admissionregv1.MatchResources
	paramKind        *admissionregv1.ParamKind
	paramRef         *admissionregv1.ParamRef
	failurePolicy    *admissionregv1.FailurePolicyType
	variables        []admissionregv1.Variable
	expressions      []string
}

func mutatingShape(policy *admissionregv1.MutatingAdmissionPolicy, binding *admissionregv1.MutatingAdmissionPolicyBinding) policyShape {
	shape := policyShape{
		constraints:   policy.Spec.MatchConstraints,
		paramKind:     policy.Spec.ParamKind,
		failurePolicy: policy.Spec.FailurePolicy,
		variables:     policy.Spec.Variables,
		expressions:   mutatingExpressions(&policy.Spec),
	}
	if binding != nil {
		shape.bindingResources, shape.paramRef = binding.Spec.MatchResources, binding.Spec.ParamRef
	}

	return shape
}

func validatingShape(policy *admissionregv1.ValidatingAdmissionPolicy, binding *admissionregv1.ValidatingAdmissionPolicyBinding) policyShape {
	shape := policyShape{
		constraints:   policy.Spec.MatchConstraints,
		paramKind:     policy.Spec.ParamKind,
		failurePolicy: policy.Spec.FailurePolicy,
		variables:     policy.Spec.Variables,
		expressions:   validatingExpressions(&policy.Spec),
	}
	if binding != nil {
		shape.bindingResources, shape.paramRef = binding.Spec.MatchResources, binding.Spec.ParamRef
	}

	return shape
}

// preflight makes the API server's checks that come before any expression is
// evaluated. It returns a result when they already decide the request: the
// policy or binding does not match, or its params are missing. An invalid
// variable reference is an error, since the server rejects such a policy.
func (e *Evaluator) preflight(
	shape policyShape,
	request *admissionv1.AdmissionRequest,
	object, oldObject, params, namespaceObj *unstructured.Unstructured,
) (*EvaluationResult, error) {
	if err := e.checkVariableReferences(shape.variables, shape.expressions); err != nil {
		return nil, err
	}

	applies, err := policyApplies(shape.constraints, shape.bindingResources, request, object, oldObject, namespaceObj)
	if err != nil {
		return nil, fmt.Errorf("evaluate match resources: %w", err)
	}

	if !applies {
		return &EvaluationResult{Allowed: true}, nil
	}

	return missingParamsResult(shape.paramKind, shape.paramRef, params, shape.failurePolicy), nil
}

// missingParamsMessage is the API server's message when a binding's paramRef
// finds no params and its parameterNotFoundAction is Deny.
const missingParamsMessage = "failed to configure binding: no params found for policy binding with `Deny` parameterNotFoundAction"

// missingParamsResult mirrors the API server when the policy has a paramKind,
// the binding has a paramRef, and no params were found: parameterNotFoundAction
// Deny rejects the request before any expression runs (unless failurePolicy is
// Ignore), and otherwise the binding is skipped. It returns nil when params are
// present or not referenced.
func missingParamsResult(
	paramKind *admissionregv1.ParamKind,
	paramRef *admissionregv1.ParamRef,
	params *unstructured.Unstructured,
	failurePolicy *admissionregv1.FailurePolicyType,
) *EvaluationResult {
	if paramKind == nil || paramRef == nil || params != nil {
		return nil
	}

	deny := paramRef.ParameterNotFoundAction != nil && *paramRef.ParameterNotFoundAction == admissionregv1.DenyAction
	ignore := failurePolicy != nil && *failurePolicy == admissionregv1.Ignore

	if deny && !ignore {
		return &EvaluationResult{Allowed: false, Message: missingParamsMessage}
	}

	return &EvaluationResult{Allowed: true}
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
	mutations []admissionregv1.Mutation,
	object *unstructured.Unstructured,
	vars map[string]any,
) (*unstructured.Unstructured, error) {
	patchedObject := object.DeepCopy()

	for _, mutation := range mutations {
		var err error

		patchedObject, err = e.applyMutation(mutation, patchedObject, vars)
		if err != nil {
			return nil, err
		}
	}

	return patchedObject, nil
}

// applyMutation applies a single mutation to object and returns the result.
func (e *Evaluator) applyMutation(
	mutation admissionregv1.Mutation,
	object *unstructured.Unstructured,
	vars map[string]any,
) (*unstructured.Unstructured, error) {
	switch mutation.PatchType {
	case admissionregv1.PatchTypeJSONPatch:
		patch, err := e.evaluateJSONPatchMutation(mutation, vars)
		if err != nil {
			return nil, err
		}

		if patch == nil {
			return object, nil
		}

		return e.applyJSONPatches(patch, object)
	case admissionregv1.PatchTypeApplyConfiguration:
		config, err := e.evaluateApplyConfigurationMutation(mutation, vars)
		if err != nil {
			return nil, err
		}

		if config == nil {
			return object, nil
		}

		return e.applyApplyConfigurations([]*unstructured.Unstructured{config}, object)
	default:
		return nil, fmt.Errorf("%w: %s", errUnsupportedPatchType, mutation.PatchType)
	}
}

// EvaluateValidating evaluates a ValidatingAdmissionPolicy against an admission request.
func (e *Evaluator) EvaluateValidating(
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
	if result, err := e.preflight(validatingShape(policy, binding), request, object, oldObject, params, namespaceObj); result != nil || err != nil {
		return result, err
	}

	// Convert admission request
	requestMap, err := convertAdmissionRequest(request)
	if err != nil {
		return nil, fmt.Errorf("convert admission request: %w", err)
	}

	// Set up CEL variables
	vars := e.setupValidatingVars(requestMap, object, oldObject, params, namespaceObj, authorizer, userInfo)

	// Bind spec.variables lazily so later expressions can reference variables.<name>
	e.bindVariables(policy.Spec.Variables, vars)

	matched, failure, err := e.evaluateMatchConditions(policy.Spec.MatchConditions, vars, policy.Spec.FailurePolicy)
	if err != nil {
		return nil, fmt.Errorf("evaluate match conditions: %w", err)
	}

	if failure != "" {
		return failuresResult([]string{failure}, binding, nil), nil
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

	failures, err := e.runValidations(policy.Spec.Validations, policy.Spec.FailurePolicy, vars)
	if err != nil {
		return nil, err
	}

	return failuresResult(failures, binding, auditAnnotations), nil
}

// runValidations evaluates every validation, as the API server does, and
// returns the failure messages in order. A validation that fails to evaluate
// is a failure unless the policy's failurePolicy is Ignore.
func (e *Evaluator) runValidations(
	validations []admissionregv1.Validation,
	failurePolicy *admissionregv1.FailurePolicyType,
	vars map[string]any,
) ([]string, error) {
	var failures []string

	for _, validation := range validations {
		result, err := e.evaluateExpression(validation.Expression, vars)

		var runtimeErr *runtimeError

		switch {
		case errors.As(err, &runtimeErr):
			if !ignoresFailures(failurePolicy) {
				failures = append(failures, runtimeErr.Error())
			}

			continue
		case err != nil:
			return nil, fmt.Errorf("evaluate validation expression %q: %w", validation.Expression, err)
		}

		allowed, ok := result.(bool)
		if !ok {
			return nil, fmt.Errorf("%w: %s returned %T", errValidationNonBoolean, validation.Expression, result)
		}

		if !allowed {
			failures = append(failures, e.validationMessage(&validation, vars))
		}
	}

	return failures, nil
}

// bindVariables binds spec.variables under the "variables" activation key as a
// lazily-evaluated map, matching how the API server evaluates composited
// variables: a variable's CEL expression runs only when it is referenced by an
// expression that actually executes, and its result is then memoized. Because
// matchConditions run before validations/mutations, a variable referenced only
// by the latter is never evaluated for a request that fails matchConditions, and
// an error in an unreachable variable never fails the case.
//
// Each variable may reference variables declared before it. That ordering is
// enforced at evaluation time by scoping every variable's activation to the
// variables that precede it, so a forward or circular reference still surfaces
// as a clear "no such key" error just as it did under eager evaluation.
func (e *Evaluator) bindVariables(variables []admissionregv1.Variable, vars map[string]any) {
	if len(variables) == 0 {
		return
	}

	variablesType := types.NewObjectType("kat.internal.evaluator.variables")
	full := lazy.NewMapValue(variablesType)

	for i := range variables {
		name := variables[i].Name
		expression := variables[i].Expression
		scope := newVariableScope(variablesType, full, variables[:i])

		full.Append(name, func(*lazy.MapValue) ref.Val {
			value, err := e.evaluateExpressionRaw(expression, activationWithVariables(vars, scope))
			if err != nil {
				return types.NewErr("evaluate variable %q: %v", name, err)
			}

			return value
		})
	}

	vars[plugin.VariableVarName] = full
}

// checkVariableReferences mirrors the API server's compile-time check of
// variables.<name> references: a variable may only reference variables
// declared before it, and the policy's other expressions only declared ones.
func (e *Evaluator) checkVariableReferences(variables []admissionregv1.Variable, expressions []string) error {
	declared := make(map[string]bool, len(variables))

	check := func(expression string) error {
		names, err := e.referencedVariables(expression)
		if err != nil {
			return err
		}

		for _, name := range names {
			if !declared[name] {
				return fmt.Errorf("compile expression %q: %w: undefined field '%s'", expression, errUndeclaredVariable, name)
			}
		}

		return nil
	}

	for _, v := range variables {
		if err := check(v.Expression); err != nil {
			return fmt.Errorf("variable %q: %w", v.Name, err)
		}

		declared[v.Name] = true
	}

	for _, expression := range expressions {
		if expression == "" {
			continue
		}

		if err := check(expression); err != nil {
			return err
		}
	}

	return nil
}

// referencedVariables returns the names selected from variables, as in
// variables.name or has(variables.name).
func (e *Evaluator) referencedVariables(expression string) ([]string, error) {
	parsed, issues := e.env.Parse(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compile expression: %w", issues.Err())
	}

	selects := celast.MatchDescendants(celast.NavigateAST(parsed.NativeRep()), func(expr celast.NavigableExpr) bool {
		if expr.Kind() != celast.SelectKind {
			return false
		}

		operand := expr.AsSelect().Operand()

		return operand.Kind() == celast.IdentKind && operand.AsIdent() == plugin.VariableVarName
	})

	names := make([]string, 0, len(selects))
	for _, s := range selects {
		names = append(names, s.AsSelect().FieldName())
	}

	return names, nil
}

func validatingExpressions(spec *admissionregv1.ValidatingAdmissionPolicySpec) []string {
	out := make([]string, 0, 2*len(spec.Validations)+len(spec.AuditAnnotations))

	for _, v := range spec.Validations {
		out = append(out, v.Expression, v.MessageExpression)
	}

	for _, a := range spec.AuditAnnotations {
		out = append(out, a.ValueExpression)
	}

	return out
}

func mutatingExpressions(spec *admissionregv1.MutatingAdmissionPolicySpec) []string {
	var out []string

	for _, m := range spec.Mutations {
		if m.JSONPatch != nil {
			out = append(out, m.JSONPatch.Expression)
		}

		if m.ApplyConfiguration != nil {
			out = append(out, m.ApplyConfiguration.Expression)
		}
	}

	return out
}

// newVariableScope returns a lazy map that exposes only the given earlier
// variables. Resolution delegates back to full so each variable is evaluated and
// memoized exactly once, no matter how many scopes reference it.
func newVariableScope(variablesType *types.Type, full *lazy.MapValue, earlier []admissionregv1.Variable) *lazy.MapValue {
	scope := lazy.NewMapValue(variablesType)

	for i := range earlier {
		name := earlier[i].Name
		scope.Append(name, func(*lazy.MapValue) ref.Val {
			return full.Get(types.String(name))
		})
	}

	return scope
}

// activationWithVariables returns a shallow copy of vars with the "variables"
// key bound to the given lazy map, leaving the caller's map untouched.
func activationWithVariables(vars map[string]any, variables *lazy.MapValue) map[string]any {
	scoped := make(map[string]any, len(vars)+1)
	maps.Copy(scoped, vars)
	scoped[plugin.VariableVarName] = variables

	return scoped
}

// evaluateMatchConditions evaluates the match conditions the way the API
// server does. Match conditions cannot reference variables, and the server
// evaluates them without a namespaceObject. Any false condition means no
// match. Otherwise, conditions that fail to evaluate produce a failure message
// under failurePolicy Fail, or no match under Ignore.
func (e *Evaluator) evaluateMatchConditions(
	conditions []admissionregv1.MatchCondition,
	vars map[string]any,
	failurePolicy *admissionregv1.FailurePolicyType,
) (bool, string, error) {
	matchVars := maps.Clone(vars)
	delete(matchVars, plugin.VariableVarName)
	matchVars[plugin.NamespaceVarName] = nil

	var failures []string

	for _, condition := range conditions {
		result, err := evaluateIn(e.matchEnv, condition.Expression, matchVars)

		var runtimeErr *runtimeError

		switch {
		case errors.As(err, &runtimeErr):
			failures = append(failures, runtimeErr.Error())

			continue
		case err != nil:
			return false, "", fmt.Errorf("evaluate match condition %q: %w", condition.Name, err)
		}

		matched, ok := result.Value().(bool)
		if !ok {
			return false, "", fmt.Errorf("%w: %s returned %T", errMatchConditionNonBoolean, condition.Name, result.Value())
		}

		if !matched {
			return false, "", nil
		}
	}

	switch {
	case len(failures) == 0:
		return true, "", nil
	case ignoresFailures(failurePolicy):
		return false, "", nil
	case len(failures) == 1:
		return false, failures[0], nil
	default:
		return false, "[" + strings.Join(failures, ", ") + "]", nil
	}
}

// runtimeError is an expression that compiled but failed to evaluate. The
// API server applies the policy's failurePolicy to these, whereas it rejects
// a policy whose expressions do not compile.
type runtimeError struct {
	expression string
	err        error
}

func (e *runtimeError) Error() string {
	return fmt.Sprintf("expression '%s' resulted in error: %v", e.expression, e.err)
}

func (e *runtimeError) Unwrap() error { return e.err }

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
	return evaluateIn(e.env, expression, vars)
}

// evaluateIn compiles and evaluates a CEL expression in env. Evaluation
// failures are returned as *runtimeError.
func evaluateIn(env *cel.Env, expression string, vars map[string]any) (ref.Val, error) {
	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("compile expression: %w", issues.Err())
	}

	prg, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("create program: %w", err)
	}

	result, _, err := prg.Eval(vars)
	if err != nil {
		return nil, &runtimeError{expression: expression, err: err}
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

// evaluateJSONPatchMutation evaluates a JSONPatch mutation and returns the CEL list.
// The CEL value must not be unwrapped: list concatenation (ext.Lists +) yields a
// native Go slice from Value(), which is not a traits.Lister.
func (e *Evaluator) evaluateJSONPatchMutation(
	mutation admissionregv1.Mutation,
	vars map[string]any,
) (ref.Val, error) {
	if mutation.JSONPatch == nil {
		//nolint:nilnil // No patch to evaluate, no error
		return nil, nil
	}

	patchResult, err := e.evaluateExpressionRaw(mutation.JSONPatch.Expression, vars)
	if err != nil {
		return nil, fmt.Errorf("evaluate JSONPatch expression: %w", err)
	}

	return patchResult, nil
}

// evaluateApplyConfigurationMutation evaluates an ApplyConfiguration mutation and returns the configuration.
func (e *Evaluator) evaluateApplyConfigurationMutation(
	mutation admissionregv1.Mutation,
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
	patch ref.Val,
	object *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	iter, ok := patch.(traits.Lister)
	if !ok {
		return nil, fmt.Errorf("%w: %T", errJSONPatchNotList, patch)
	}

	result := jsonpatch.Patch{}
	if err := appendPatchOperations(iter.Iterator(), &result); err != nil {
		return nil, err
	}

	if len(result) == 0 {
		return object.DeepCopy(), nil
	}

	return applyPatchOperations(result, object)
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
	patchObj, err := value.ConvertToNative(reflect.TypeFor[*mutation.JSONPatchVal]())
	if err != nil {
		return nil, fmt.Errorf("convert patch element: %w", err)
	}

	op, ok := patchObj.(*mutation.JSONPatchVal)
	if !ok {
		return nil, fmt.Errorf("%w: %T", errUnexpectedPatchType, patchObj)
	}

	resultOp := jsonpatch.Operation{}
	resultOp["op"] = new(json.RawMessage(strconv.Quote(op.Op)))
	resultOp["path"] = new(json.RawMessage(strconv.Quote(op.Path)))

	if len(op.From) > 0 {
		resultOp["from"] = new(json.RawMessage(strconv.Quote(op.From)))
	}

	if op.Val != nil {
		// Convert to native Go directly; cel-go's structpb.Value conversion
		// panics when the value is a list of objects.
		converted := convertCELValue(op.Val)

		b, err := json.Marshal(converted)
		if err != nil {
			return nil, fmt.Errorf("marshal patch value: %w", err)
		}

		resultOp["value"] = new(json.RawMessage(b))
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

// applyApplyConfigurations applies ApplyConfiguration configs to an object using
// Kubernetes server-side-apply semantics (see the ssa package): lists declared
// as listType=map in the schema are merged by their keys rather than replaced.
func (e *Evaluator) applyApplyConfigurations(
	configs []*unstructured.Unstructured,
	object *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	result := object

	for _, config := range configs {
		merged, err := ssa.Merge(result, config)
		if err != nil {
			return nil, fmt.Errorf("apply configuration: %w", err)
		}

		result = merged
	}

	return result, nil
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
