package evaluator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/pmezard/go-difflib/difflib"
	"gopkg.in/yaml.v3"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

var errNoPolicy = errors.New("no policy provided")

const diffContextLines = 3

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

// Policies is the policy a test case is evaluated against, with all of its
// bindings. Exactly one of Mutating and Validating is set. A policy without
// bindings is evaluated as if bound once with the Deny action and no params.
type Policies struct {
	Mutating           *admissionregv1.MutatingAdmissionPolicy
	MutatingBindings   []*admissionregv1.MutatingAdmissionPolicyBinding
	Validating         *admissionregv1.ValidatingAdmissionPolicy
	ValidatingBindings []*admissionregv1.ValidatingAdmissionPolicyBinding
}

// EvaluateTest evaluates a policy against a test case and returns whether it passed.
func (e *Evaluator) EvaluateTest(ctx context.Context, policies Policies, testCase TestCase) *TestResult {
	expected := TestExpectation{
		Allowed:          testCase.GetExpectAllowed(),
		Message:          testCase.GetExpectMessage(),
		Object:           testCase.GetExpectedObject(),
		Warnings:         testCase.GetExpectWarnings(),
		AuditAnnotations: testCase.GetExpectAuditAnnotations(),
	}

	if err := testCase.GetError(); err != nil {
		return &TestResult{Expected: expected, Message: fmt.Sprintf("test loading error: %v", err)}
	}

	evalResult, err := e.Evaluate(ctx, policies, testCase)
	if invalid, ok := errors.AsType[*InvalidPolicyError](err); ok {
		return &TestResult{Expected: expected, Message: "the API server would reject the policy: " + invalid.Error()}
	}

	if err != nil {
		return &TestResult{Expected: expected, Message: fmt.Sprintf("evaluation error: %v", err)}
	}

	actual := TestOutcome{
		Allowed:          evalResult.Allowed,
		Message:          evalResult.Message,
		Warnings:         evalResult.Warnings,
		AuditAnnotations: evalResult.AuditAnnotations,
		Object:           testCase.GetObject(),
	}

	if evalResult.PatchedObject != nil {
		actual.Object = evalResult.PatchedObject
	}

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

// Evaluate admits the test case's request with the policy the way the API
// server would, without comparing the result to the test case's
// expectations. A policy or binding the server would reject at creation
// returns an *InvalidPolicyError.
func (e *Evaluator) Evaluate(ctx context.Context, policies Policies, testCase TestCase) (*EvaluationResult, error) {
	var authz authorizer.Authorizer
	if configs := testCase.GetAuthorizer(); len(configs) > 0 {
		authz = NewMockAuthorizerFromConfig(configs)
	}

	req, err := newRequest(testCase.GetRequest(), testCase.GetObject(), testCase.GetOldObject(),
		testCase.GetParams(), testCase.GetNamespaceObj(), authz, testCase.GetUserInfo())
	if err != nil {
		return nil, err
	}

	switch {
	case policies.Mutating != nil:
		return e.admitMutating(ctx, policies.Mutating, policies.MutatingBindings, req)
	case policies.Validating != nil:
		return e.admitValidating(ctx, policies.Validating, policies.ValidatingBindings, req)
	default:
		return nil, errNoPolicy
	}
}

// admitValidating creates the policy and its bindings as the API server
// would, defaulting and validating them, then admits the request.
func (e *Evaluator) admitValidating(
	ctx context.Context,
	policy *admissionregv1.ValidatingAdmissionPolicy,
	bindings []*admissionregv1.ValidatingAdmissionPolicyBinding,
	req *request,
) (*EvaluationResult, error) {
	policy = policy.DeepCopy()
	defaultValidatingPolicy(policy)

	if err := e.validateValidatingPolicy(policy); err != nil {
		return nil, err
	}

	created := make([]*admissionregv1.ValidatingAdmissionPolicyBinding, 0, len(bindings))

	for _, b := range bindings {
		b = b.DeepCopy()
		defaultMatchResources(b.Spec.MatchResources)

		if err := validateValidatingBinding(b); err != nil {
			return nil, err
		}

		created = append(created, b)
	}

	if len(created) == 0 {
		created = append(created, &admissionregv1.ValidatingAdmissionPolicyBinding{
			Name: policy.Name,
			Spec: admissionregv1.ValidatingAdmissionPolicyBindingSpec{
				PolicyName:        policy.Name,
				ValidationActions: []admissionregv1.ValidationAction{admissionregv1.Deny},
			},
		})
	}

	return e.evaluateValidating(ctx, policy, created, req)
}

// admitMutating is admitValidating for a MutatingAdmissionPolicy.
func (e *Evaluator) admitMutating(
	ctx context.Context,
	policy *admissionregv1.MutatingAdmissionPolicy,
	bindings []*admissionregv1.MutatingAdmissionPolicyBinding,
	req *request,
) (*EvaluationResult, error) {
	policy = policy.DeepCopy()
	defaultMutatingPolicy(policy)

	if err := e.validateMutatingPolicy(policy); err != nil {
		return nil, err
	}

	created := make([]*admissionregv1.MutatingAdmissionPolicyBinding, 0, len(bindings))

	for _, b := range bindings {
		b = b.DeepCopy()
		defaultMatchResources(b.Spec.MatchResources)

		if err := validateMutatingBinding(b); err != nil {
			return nil, err
		}

		created = append(created, b)
	}

	if len(created) == 0 {
		created = append(created, &admissionregv1.MutatingAdmissionPolicyBinding{
			Name: policy.Name,
			Spec: admissionregv1.MutatingAdmissionPolicyBindingSpec{PolicyName: policy.Name},
		})
	}

	return e.evaluateMutating(ctx, policy, created, req)
}

// checkWarnings verifies that actual warnings match expected warnings.
// Returns a TestResult on mismatch, or nil if all checks pass.
func checkWarnings(expected, actual []string) *TestResult {
	switch {
	case expected == nil:
		return nil
	case len(expected) == 0 && len(actual) > 0:
		return &TestResult{
			Passed:  false,
			Message: fmt.Sprintf("expected no warnings, got %q", actual),
		}
	case len(expected) == 0:
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
	switch {
	case expected.AuditAnnotations == nil:
		return nil
	case len(expected.AuditAnnotations) == 0:
		if len(actual.AuditAnnotations) > 0 {
			return &TestResult{Message: fmt.Sprintf("expected no audit annotations, got %v", actual.AuditAnnotations)}
		}

		return nil
	}

	result := &TestResult{}

	// Filter actual to only contain keys from expected, to ignore extra annotations
	actualFiltered := make(map[string]string)

	for k := range expected.AuditAnnotations {
		if v, ok := actual.AuditAnnotations[k]; ok {
			actualFiltered[k] = v
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

	// Compare as JSON: numbers parsed from YAML and produced by a patch differ in Go type.
	if !jsonEqual(expected.Object.Object, actual.Object.Object) {
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

func jsonEqual(a, b any) bool {
	aJSON, aErr := json.Marshal(a)
	bJSON, bErr := json.Marshal(b)

	return aErr == nil && bErr == nil && bytes.Equal(aJSON, bJSON)
}

// EvaluationResult is the API server's admission decision for a request.
type EvaluationResult struct {
	Allowed bool
	// Message is the denial message of the first denying decision, without
	// the server's "<policy> with binding <binding> denied request:" prefix.
	Message string
	// Binding names the binding of the denying decision.
	Binding string
	// Reason is the denial's status reason.
	Reason metav1.StatusReason
	// Warnings are the failure messages of Warn bindings, without the
	// server's "Validation failed for ..." prefix.
	Warnings []string
	// PatchedObject is the mutated object, nil when it did not change.
	PatchedObject    *unstructured.Unstructured
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
}
