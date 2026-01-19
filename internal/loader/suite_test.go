package loader

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/zemanlx/kat/internal/evaluator"
)

var errTest = errors.New("test error")

func TestDiscoverTestSuites(t *testing.T) {
	t.Parallel()

	// Use the test-policies-pass directory from the workspace
	testPoliciesDir := filepath.Join("..", "..", "test-policies-pass")

	suites, err := DiscoverTestSuites(testPoliciesDir)
	if err != nil {
		t.Fatalf("DiscoverTestSuites() error = %v", err)
	}

	if len(suites) == 0 {
		t.Fatal("Expected to find test suites, got 0")
	}

	t.Logf("Discovered %d test suites", len(suites))

	// Verify each suite has required components
	for _, suite := range suites {
		s := suite
		t.Run(s.Name, func(t *testing.T) {
			t.Parallel()
			t.Logf("Suite: %s", suite.Name)
			t.Logf("  Path: %s", suite.Path)
			t.Logf("  Mutating policies: %d", len(suite.MutatingPolicies))
			t.Logf("  Mutating bindings: %d", len(suite.MutatingBindings))
			t.Logf("  Validating policies: %d", len(suite.ValidatingPolicies))
			t.Logf("  Validating bindings: %d", len(suite.ValidatingBindings))
			t.Logf("  Test requests: %d", len(suite.Tests))

			// Each suite should have at least one policy or binding
			totalPolicies := len(s.MutatingPolicies) + len(s.ValidatingPolicies)
			totalBindings := len(s.MutatingBindings) + len(s.ValidatingBindings)

			if totalPolicies == 0 {
				t.Errorf("Suite %s has no policies", s.Name)
			}

			if totalBindings == 0 {
				t.Errorf("Suite %s has no bindings", s.Name)
			}
		})
	}
}

//nolint:cyclop,funlen // Table-driven test with multiple scenarios
func TestLoadTestSuite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		dir            string
		expectPolicies bool
		expectTests    bool
		minPolicies    int
		minBindings    int
	}{
		{
			name:           "add-default-labels",
			dir:            filepath.Join("..", "..", "test-policies-pass", "mutating", "add-default-labels"),
			expectPolicies: true,
			expectTests:    true,
			minPolicies:    1,
			minBindings:    1,
		},
		{
			name:           "block-pod-exec",
			dir:            filepath.Join("..", "..", "test-policies-pass", "validating", "block-pod-exec"),
			expectPolicies: true,
			expectTests:    true,
			minPolicies:    1,
			minBindings:    1,
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			suite, err := LoadTestSuite(tc.dir, tc.name)
			if err != nil {
				t.Fatalf("LoadTestSuite() error = %v", err)
			}

			if suite == nil {
				t.Fatal("LoadTestSuite() returned nil suite")
			}

			if suite.Name != tc.name {
				t.Errorf("Suite name = %s, want %s", suite.Name, tc.name)
			}

			totalPolicies := len(suite.MutatingPolicies) + len(suite.ValidatingPolicies)
			if tc.expectPolicies && totalPolicies < tc.minPolicies {
				t.Errorf("Expected at least %d policies, got %d", tc.minPolicies, totalPolicies)
			}

			totalBindings := len(suite.MutatingBindings) + len(suite.ValidatingBindings)
			if tc.expectPolicies && totalBindings < tc.minBindings {
				t.Errorf("Expected at least %d bindings, got %d", tc.minBindings, totalBindings)
			}

			if tc.expectTests && len(suite.Tests) == 0 {
				t.Errorf("Expected test requests, got 0")
			}

			t.Logf("Loaded suite %s with %d policies, %d bindings, %d tests",
				suite.Name, totalPolicies, totalBindings, len(suite.Tests))

			// Verify test files are matched to policies by name
			for _, testReq := range suite.Tests {
				if testReq.PolicyName == "" {
					t.Logf("Warning: test file %s not matched to any policy", testReq.Name)
				}
			}
		})
	}
}

//nolint:cyclop,funlen // Getter test
func TestTestCase_Getters(t *testing.T) {
	t.Parallel()

	req := &admissionv1.AdmissionRequest{UID: "uid"}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{"kind": "Pod"}}
	oldObj := &unstructured.Unstructured{Object: map[string]interface{}{"kind": "Pod"}}
	params := &unstructured.Unstructured{Object: map[string]interface{}{"foo": "bar"}}
	nsObj := &unstructured.Unstructured{Object: map[string]interface{}{"kind": "Namespace"}}
	userInfo := &user.DefaultInfo{Name: "user"}
	auth := []evaluator.AuthorizationMockConfig{{Verb: "get"}}
	expectedObj := &unstructured.Unstructured{Object: map[string]interface{}{"kind": "Mutation"}}
	err := errTest

	tc := &TestCase{
		Request:                req,
		Object:                 obj,
		OldObject:              oldObj,
		Params:                 params,
		NamespaceObj:           nsObj,
		UserInfo:               userInfo,
		Authorizer:             auth,
		ExpectAllowed:          true,
		ExpectMessage:          "msg",
		ExpectWarnings:         []string{"warn"},
		ExpectAuditAnnotations: map[string]string{"k": "v"},
		ExpectedObject:         expectedObj,
		Error:                  err,
	}

	if tc.GetRequest() != req {
		t.Error("GetRequest mismatch")
	}

	if tc.GetObject() != obj {
		t.Error("GetObject mismatch")
	}

	if tc.GetOldObject() != oldObj {
		t.Error("GetOldObject mismatch")
	}

	if tc.GetParams() != params {
		t.Error("GetParams mismatch")
	}

	if tc.GetNamespaceObj() != nsObj {
		t.Error("GetNamespaceObj mismatch")
	}

	if tc.GetUserInfo() != userInfo {
		t.Error("GetUserInfo mismatch")
	}

	if !cmp.Equal(tc.GetAuthorizer(), auth) {
		t.Error("GetAuthorizer mismatch")
	}

	if tc.GetExpectAllowed() != true {
		t.Error("GetExpectAllowed mismatch")
	}

	if tc.GetExpectMessage() != "msg" {
		t.Error("GetExpectMessage mismatch")
	}

	if !cmp.Equal(tc.GetExpectWarnings(), []string{"warn"}) {
		t.Error("GetExpectWarnings mismatch")
	}

	if !cmp.Equal(tc.GetExpectAuditAnnotations(), map[string]string{"k": "v"}) {
		t.Error("GetExpectAuditAnnotations mismatch")
	}

	if tc.GetExpectedObject() != expectedObj {
		t.Error("GetExpectedObject mismatch")
	}

	if !errors.Is(tc.GetError(), err) {
		t.Error("GetError mismatch")
	}
}

func TestFilterTestsByPattern(t *testing.T) {
	t.Parallel()

	suites := []*TestSuite{
		{
			Name: "suite1",
			Tests: []*TestCase{
				{Name: "test1"},
				{Name: "test2"},
			},
		},
		{
			Name: "suite2",
			Tests: []*TestCase{
				{Name: "test3"},
				{Name: "other"},
			},
		},
	}

	tests := []struct {
		name          string
		pattern       string
		expectedCount int
	}{
		{
			name:          "match all",
			pattern:       ".*",
			expectedCount: 2, // 2 suites (with matching tests)
		},
		{
			name:          "match specific test",
			pattern:       "test1",
			expectedCount: 1, // suite1 only
		},
		{
			name:          "match test prefix",
			pattern:       "test",
			expectedCount: 2, // both suites have "test..." cases
		},
		{
			name:          "no match",
			pattern:       "nomatch",
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			filtered := filterTestsByPattern(copySuites(suites), tt.pattern)
			if len(filtered) != tt.expectedCount {
				t.Errorf("Expected %d suites, got %d", tt.expectedCount, len(filtered))
			}
		})
	}
}

func copySuites(suites []*TestSuite) []*TestSuite {
	cp := make([]*TestSuite, len(suites))

	for i, s := range suites {
		testsCp := make([]*TestCase, len(s.Tests))
		copy(testsCp, s.Tests)
		cp[i] = &TestSuite{
			Name:  s.Name,
			Tests: testsCp,
		}
	}

	return cp
}

func TestLoad_Patterns(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a dummy policy structure
	// suite1/policy.yaml
	// suite1/tests/test1.yaml
	suite1Dir := filepath.Join(tmpDir, "suite1")
	mustMkdir(t, suite1Dir)

	if err := os.WriteFile(filepath.Join(suite1Dir, "policy.yaml"), []byte("apiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingAdmissionPolicy\nmetadata:\n  name: p1\nspec:\n  validations:\n  - expression: 'true'"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create tests dir and a dummy test file that references a policy name
	testsDir := filepath.Join(suite1Dir, "tests")
	mustMkdir(t, testsDir)
	// We need a valid test file structure for it to be loaded
	testFile := `
apiVersion: admission.k8s.io/v1
kind: AdmissionRequest
uid: 123
operation: CREATE
object:
  apiVersion: v1
  kind: Pod
  metadata:
    name: foo
`
	if err := os.WriteFile(filepath.Join(testsDir, "p1.allow.request.yaml"), []byte(testFile), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Test 1: Load specific pattern
	suites, err := Load(tmpDir, "p1")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if len(suites) != 1 {
		t.Errorf("Expected 1 suite, got %d", len(suites))
	} else if len(suites[0].Tests) != 1 {
		t.Errorf("Expected 1 test in suite, got %d", len(suites[0].Tests))
	}

	// Test 2: Load non-matching pattern
	suites, err = Load(tmpDir, "nopattern")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if len(suites) != 0 {
		t.Errorf("Expected 0 suites, got %d", len(suites))
	}

	// Test 3: Load directly from suite dir
	suites, err = Load(suite1Dir, "")
	if err != nil {
		t.Fatalf("Load direct error: %v", err)
	}

	if len(suites) != 1 {
		t.Errorf("Expected 1 suite, got %d", len(suites))
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
}

func TestConvertUserInfo(t *testing.T) {
	t.Parallel()

	// Test nil input
	if got := convertUserInfo(nil); got != nil {
		t.Errorf("Expected nil, got %v", got)
	}

	// Test full input
	input := &authenticationv1.UserInfo{
		Username: "user1",
		UID:      "uid1",
		Groups:   []string{"group1"},
		Extra:    map[string]authenticationv1.ExtraValue{"key": {"val1", "val2"}},
	}

	got := convertUserInfo(input)
	if got == nil {
		t.Fatal("Expected non-nil result")
	}

	if got.GetName() != "user1" {
		t.Errorf("Expected username user1, got %s", got.GetName())
	}

	if got.GetUID() != "uid1" {
		t.Errorf("Expected uid uid1, got %s", got.GetUID())
	}

	if !cmp.Equal(got.GetGroups(), []string{"group1"}) {
		t.Errorf("Groups mismatch")
	}

	expectedExtra := map[string][]string{"key": {"val1", "val2"}}
	if !cmp.Equal(got.GetExtra(), expectedExtra) {
		t.Errorf("Extra mismatch: %v", got.GetExtra())
	}
}

func TestMatchPolicyName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		baseName    string
		policyNames []string
		expected    string
	}{
		{
			name:        "single policy",
			baseName:    "anything",
			policyNames: []string{"p1"},
			expected:    "p1",
		},
		{
			name:        "match prefix",
			baseName:    "p1.allow.anything",
			policyNames: []string{"p1", "p2"},
			expected:    "p1",
		},
		{
			name:        "match second prefix",
			baseName:    "p2.allow.anything",
			policyNames: []string{"p1", "p2"},
			expected:    "p2",
		},
		{
			name:        "no match",
			baseName:    "other.allow",
			policyNames: []string{"p1", "p2"},
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := matchPolicyName(tt.baseName, tt.policyNames)
			if got != tt.expected {
				t.Errorf("matchPolicyName(%q, %v) = %q; want %q", tt.baseName, tt.policyNames, got, tt.expected)
			}
		})
	}
}

func TestExpectedAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		baseName string
		expected bool
	}{
		{"test.deny", false},
		{"test.deny.something", false},
		{"test.audit", true},
		{"test.warn", true},
		{"test.allow", true},
		{"test.other", true}, // default true
	}

	for _, tt := range tests {
		t.Run(tt.baseName, func(t *testing.T) {
			t.Parallel()

			if got := expectedAllowed(tt.baseName); got != tt.expected {
				t.Errorf("expectedAllowed(%q) = %v; want %v", tt.baseName, got, tt.expected)
			}
		})
	}
}

func TestIsTestFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fileName string
		expected bool
	}{
		{"request", "test.request.yaml", true},
		{"object", "test.object.yaml", true},
		{"oldObject", "test.oldObject.yaml", true},
		{"params", "test.params.yaml", true},
		{"annotations", "test.annotations.yaml", true},
		{"warnings", "test.warnings.txt", true},
		{"authorizer", "test.authorizer.yaml", true},
		{"unknown", "test.unknown.yaml", false},
		{"no extension", "test", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isTestFile(tt.fileName); got != tt.expected {
				t.Errorf("isTestFile(%q) = %v; want %v", tt.fileName, got, tt.expected)
			}
		})
	}
}

func TestMergeTestRequests(t *testing.T) {
	t.Parallel()

	// Base request
	testReq := &testRequest{
		Name:     "base",
		Request:  &admissionv1.AdmissionRequest{Operation: "CREATE"},
		UserInfo: &authenticationv1.UserInfo{Username: "base"},
	}

	// Request to merge from (overrides)
	tempReq := &testRequest{
		Name:          "temp",
		Request:       &admissionv1.AdmissionRequest{Operation: "UPDATE"},
		UserInfo:      &authenticationv1.UserInfo{Username: "override"},
		ExpectMessage: "merged",
	}

	mergeTestRequests(testReq, tempReq)

	if testReq.Request.Operation != "UPDATE" {
		t.Errorf("Expected Operation merged to UPDATE, got %s", testReq.Request.Operation)
	}

	if testReq.UserInfo.Username != "override" {
		t.Errorf("Expected Username merged to override, got %s", testReq.UserInfo.Username)
	}

	if testReq.ExpectMessage != "merged" {
		t.Errorf("Expected ExpectMessage merged to merged, got %s", testReq.ExpectMessage)
	}
}

func TestLoad_Errors(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// Create invalid policy file
	suiteDir := filepath.Join(tmpDir, "badsuite")
	mustMkdir(t, suiteDir)

	if err := os.WriteFile(filepath.Join(suiteDir, "policy.yaml"), []byte("invalid: yaml: :"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := Load(suiteDir, "")
	if err == nil {
		t.Error("Expected error loading invalid policy")
	}
}

func TestDiscover_Recursive(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// parent/child/policy.yaml
	parent := filepath.Join(tmpDir, "parent")
	child := filepath.Join(parent, "child")
	mustMkdir(t, child)

	if err := os.WriteFile(filepath.Join(child, "policy.yaml"), []byte("apiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingAdmissionPolicy\nmetadata:\n  name: p1\nspec:\n  validations:\n  - expression: 'true'"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	suites, err := Load(tmpDir, "")
	if err != nil {
		t.Fatalf("Load recursive error: %v", err)
	}

	if len(suites) != 1 {
		t.Errorf("Expected 1 suite found recursively, got %d", len(suites))
	} else if suites[0].Name != "child" {
		t.Errorf("Expected suite name child, got %s", suites[0].Name)
	}
}

func TestMergeRequest_AllFields(t *testing.T) {
	t.Parallel()

	testReq := &testRequest{
		Request: &admissionv1.AdmissionRequest{
			Operation: "CREATE",
			Resource:  testGroupVersionResource("v1", "pods"),
		},
	}

	// Request to merge with all fields set
	tempReq := &testRequest{
		Request: &admissionv1.AdmissionRequest{
			Operation:   "UPDATE",
			SubResource: "status",
			Namespace:   "ns1",
			Name:        "name1",
			UserInfo:    authenticationv1.UserInfo{Username: "user2"},
			Resource:    testGroupVersionResource("v2", "deployments"),
			Kind:        testGroupVersionKind("v2", "Deployment"),
		},
	}
	// Manually set Raw for Options as it is json.RawMessage ([]byte)
	tempReq.Request.Options.Raw = []byte("{}")

	mergeRequest(testReq, tempReq)

	if testReq.Request.Operation != "UPDATE" {
		t.Error("Operation not merged")
	}

	if testReq.Request.SubResource != "status" {
		t.Error("SubResource not merged")
	}

	if testReq.Request.Namespace != "ns1" {
		t.Error("Namespace not merged")
	}

	if testReq.Request.Name != "name1" {
		t.Error("Name not merged")
	}

	if testReq.Request.UserInfo.Username != "user2" {
		t.Error("UserInfo not merged")
	}

	if testReq.Request.Resource.Resource != "deployments" {
		t.Error("Resource not merged")
	}

	if testReq.Request.Kind.Kind != "Deployment" {
		t.Error("Kind not merged")
	}

	if testReq.Request.Options.Raw == nil {
		t.Error("Options not merged")
	}
}

func testGroupVersionResource(version, resource string) metav1.GroupVersionResource {
	return metav1.GroupVersionResource{Version: version, Resource: resource}
}

func testGroupVersionKind(version, kind string) metav1.GroupVersionKind {
	return metav1.GroupVersionKind{Version: version, Kind: kind}
}
