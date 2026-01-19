package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	admissionv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/authentication/user"
)

// TestSuite represents a policy directory with its policies, bindings, and test cases.
type TestSuite struct {
	Name               string
	Path               string
	MutatingPolicies   []*admissionv1beta1.MutatingAdmissionPolicy
	MutatingBindings   []*admissionv1beta1.MutatingAdmissionPolicyBinding
	ValidatingPolicies []*admissionregv1.ValidatingAdmissionPolicy
	ValidatingBindings []*admissionregv1.ValidatingAdmissionPolicyBinding
	Tests              []*TestCase
}

// TestCase represents a single test case with all inputs and expected outcomes.
type TestCase struct {
	Name       string
	FilePath   string
	PolicyName string

	// Inputs for evaluation
	Request      *admissionv1.AdmissionRequest
	Object       *unstructured.Unstructured
	OldObject    *unstructured.Unstructured
	Params       *unstructured.Unstructured
	NamespaceObj *unstructured.Unstructured
	UserInfo     user.Info

	// Expected outcomes
	ExpectAllowed          bool
	ExpectMessage          string
	ExpectWarnings         []string
	ExpectAuditAnnotations map[string]string
	ExpectMutated          bool
	ExpectedObject         *unstructured.Unstructured
	Error                  error
}

// Getter methods for TestCase to satisfy evaluator.TestCase interface.
func (tc *TestCase) GetRequest() *admissionv1.AdmissionRequest     { return tc.Request }
func (tc *TestCase) GetObject() *unstructured.Unstructured         { return tc.Object }
func (tc *TestCase) GetOldObject() *unstructured.Unstructured      { return tc.OldObject }
func (tc *TestCase) GetParams() *unstructured.Unstructured         { return tc.Params }
func (tc *TestCase) GetNamespaceObj() *unstructured.Unstructured   { return tc.NamespaceObj }
func (tc *TestCase) GetUserInfo() user.Info                        { return tc.UserInfo }
func (tc *TestCase) GetExpectAllowed() bool                        { return tc.ExpectAllowed }
func (tc *TestCase) GetExpectMessage() string                      { return tc.ExpectMessage }
func (tc *TestCase) GetExpectWarnings() []string                   { return tc.ExpectWarnings }
func (tc *TestCase) GetExpectAuditAnnotations() map[string]string  { return tc.ExpectAuditAnnotations }
func (tc *TestCase) GetExpectedObject() *unstructured.Unstructured { return tc.ExpectedObject }
func (tc *TestCase) GetError() error                               { return tc.Error }

// testRequest represents a test admission request with expected outcome (internal use only).
type testRequest struct {
	Name       string
	FilePath   string
	PolicyName string

	// Input
	Request       *admissionv1.AdmissionRequest
	Object        *unstructured.Unstructured
	OldObject     *unstructured.Unstructured
	Params        *unstructured.Unstructured
	NamespaceName string
	NamespaceObj  *unstructured.Unstructured
	UserInfo      *authenticationv1.UserInfo

	// Expected outcomes
	ExpectAllowed          bool
	ExpectMessage          string
	ExpectWarnings         []string
	ExpectAuditAnnotations map[string]string
	ExpectMutated          bool
	ExpectedObject         *unstructured.Unstructured
	Error                  error
}

// Load discovers and loads all test suites from the given path.
// Pattern is optional and filters tests by name (like -run flag in go test).
func Load(path string, pattern string) ([]*TestSuite, error) {
	// Check if path is a single test suite (has policy files directly)
	hasPolicies, err := hasPolicyFiles(path)
	if err != nil {
		return nil, err
	}

	var suites []*TestSuite

	if hasPolicies {
		// Load single test suite
		suiteName := filepath.Base(path)

		suite, err := LoadTestSuite(path, suiteName)
		if err != nil {
			return nil, fmt.Errorf("load test suite: %w", err)
		}

		if suite != nil {
			suites = []*TestSuite{suite}
		}
	} else {
		// Discover multiple test suites
		suites, err = DiscoverTestSuites(path)
		if err != nil {
			return nil, err
		}
	}

	// Filter by pattern if provided
	if pattern != "" {
		suites = filterTestsByPattern(suites, pattern)
	}

	return suites, nil
}

// convertToTestCases converts testRequest to TestCase format.
func convertToTestCases(requests []*testRequest) []*TestCase {
	tests := make([]*TestCase, len(requests))
	for i, req := range requests {
		tests[i] = &TestCase{
			Name:                   req.Name,
			FilePath:               req.FilePath,
			PolicyName:             req.PolicyName,
			Request:                req.Request,
			Object:                 req.Object,
			OldObject:              req.OldObject,
			Params:                 req.Params,
			NamespaceObj:           req.NamespaceObj,
			UserInfo:               convertUserInfo(req.UserInfo),
			ExpectAllowed:          req.ExpectAllowed,
			ExpectMessage:          req.ExpectMessage,
			ExpectWarnings:         req.ExpectWarnings,
			ExpectAuditAnnotations: req.ExpectAuditAnnotations,
			ExpectMutated:          req.ExpectMutated,
			ExpectedObject:         req.ExpectedObject,
			Error:                  req.Error,
		}
	}

	return tests
}

// filterTestsByPattern filters test suites and their tests by a glob pattern.
func filterTestsByPattern(suites []*TestSuite, pattern string) []*TestSuite {
	filtered := make([]*TestSuite, 0, len(suites))

	for _, suite := range suites {
		filteredTests := make([]*TestCase, 0, len(suite.Tests))

		for _, test := range suite.Tests {
			matched, _ := regexp.MatchString(pattern, test.Name)
			if matched {
				filteredTests = append(filteredTests, test)
			}
		}

		if len(filteredTests) > 0 {
			suite.Tests = filteredTests
			filtered = append(filtered, suite)
		}
	}

	return filtered
}

// convertUserInfo converts authenticationv1.UserInfo to user.Info interface.
func convertUserInfo(info *authenticationv1.UserInfo) user.Info {
	if info == nil {
		return nil
	}

	return &user.DefaultInfo{
		Name:   info.Username,
		UID:    info.UID,
		Groups: info.Groups,
		Extra:  convertExtra(info.Extra),
	}
}

// convertExtra converts map[string]authenticationv1.ExtraValue to map[string][]string.
func convertExtra(extra map[string]authenticationv1.ExtraValue) map[string][]string {
	if extra == nil {
		return nil
	}

	result := make(map[string][]string, len(extra))
	for k, v := range extra {
		result[k] = []string(v)
	}

	return result
}

// DiscoverTestSuites discovers all policy test suites in a directory.
// Each subdirectory with policy files is considered a test suite.
// Test requests are loaded from the tests/ subdirectory if present.
func DiscoverTestSuites(rootDir string) ([]*TestSuite, error) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", rootDir, err)
	}

	suites := make([]*TestSuite, 0, len(entries))

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if err := collectSuitesFromEntry(&suites, rootDir, entry); err != nil {
			return nil, err
		}
	}

	return suites, nil
}

func collectSuitesFromEntry(suites *[]*TestSuite, rootDir string, entry os.DirEntry) error {
	dirName := entry.Name()
	if shouldSkipDir(dirName) {
		return nil
	}

	suiteDir := filepath.Join(rootDir, dirName)

	hasPolicies, err := hasPolicyFiles(suiteDir)
	if err != nil {
		return err
	}

	if !hasPolicies {
		subSuites, err := DiscoverTestSuites(suiteDir)
		if err != nil {
			return err
		}

		*suites = append(*suites, subSuites...)

		return nil
	}

	suite, err := LoadTestSuite(suiteDir, dirName)
	if err != nil {
		return fmt.Errorf("failed to load test suite %s: %w", dirName, err)
	}

	if suite != nil {
		*suites = append(*suites, suite)
	}

	return nil
}

func shouldSkipDir(dirName string) bool {
	return strings.HasPrefix(dirName, ".") || dirName == "tests" || dirName == "testdata"
}

// LoadTestSuite loads policies, bindings, and test requests from a directory.
func LoadTestSuite(dir string, name string) (*TestSuite, error) {
	suite := &TestSuite{
		Name: name,
		Path: dir,
	}

	// Load policies and bindings from the directory
	policySet, err := LoadPolicySet(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to load policies: %w", err)
	}

	suite.MutatingPolicies = policySet.MutatingPolicies
	suite.MutatingBindings = policySet.MutatingBindings
	suite.ValidatingPolicies = policySet.ValidatingPolicies
	suite.ValidatingBindings = policySet.ValidatingBindings

	// Check if there's a tests/ subdirectory
	testsDir := filepath.Join(dir, "tests")
	if info, err := os.Stat(testsDir); err == nil && info.IsDir() {
		// Collect policy names for matching test files
		policyNames := make([]string, 0)
		for _, p := range suite.MutatingPolicies {
			policyNames = append(policyNames, p.Name)
		}

		for _, p := range suite.ValidatingPolicies {
			policyNames = append(policyNames, p.Name)
		}

		// Load test requests from tests/ directory
		testRequests, err := loadTestRequests(testsDir, policyNames)
		if err != nil {
			return nil, fmt.Errorf("failed to load test requests: %w", err)
		}

		suite.Tests = convertToTestCases(testRequests)
	}

	return suite, nil
}

// loadTestRequests loads test admission requests from a directory.
// Test files are expected to be YAML files containing either:
// - AdmissionRequest objects (*.request.yaml)
// - Kubernetes objects that will be converted to AdmissionRequests (*.object.yaml)
// Expected outcomes can be specified in corresponding *.gold.yaml files.
// Test file names should be prefixed with the policy name (e.g., "policy-name.test-case.request.yaml").
// Files with the same base name (e.g., "test.allow.object.yaml" and "test.allow.request.yaml") are merged.
func loadTestRequests(dir string, policyNames []string) ([]*testRequest, error) {
	testFiles, err := collectTestFiles(dir)
	if err != nil {
		return nil, err
	}

	// Sort baseNames for deterministic order
	baseNames := make([]string, 0, len(testFiles))
	for baseName := range testFiles {
		baseNames = append(baseNames, baseName)
	}

	sort.Strings(baseNames)

	requests := make([]*testRequest, 0, len(testFiles))

	for _, baseName := range baseNames {
		filePaths := testFiles[baseName]
		req := buildTestRequest(baseName, filePaths, policyNames)
		requests = append(requests, req)
	}

	return requests, nil
}

func collectTestFiles(dir string) (map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read tests directory %s: %w", dir, err)
	}

	testFiles := make(map[string][]string)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !isTestFile(name) {
			continue
		}

		baseName := testBaseName(name)
		testFiles[baseName] = append(testFiles[baseName], filepath.Join(dir, name))
	}

	return testFiles, nil
}

func isTestFile(name string) bool {
	return strings.HasSuffix(name, ".request.yaml") ||
		strings.HasSuffix(name, ".object.yaml") ||
		strings.HasSuffix(name, ".oldObject.yaml") ||
		strings.HasSuffix(name, ".params.yaml") ||
		strings.HasSuffix(name, ".annotations.yaml") ||
		strings.HasSuffix(name, ".warnings.txt")
}

func testBaseName(name string) string {
	baseName := strings.TrimSuffix(name, ".request.yaml")
	baseName = strings.TrimSuffix(baseName, ".object.yaml")
	baseName = strings.TrimSuffix(baseName, ".oldObject.yaml")
	baseName = strings.TrimSuffix(baseName, ".params.yaml")
	baseName = strings.TrimSuffix(baseName, ".annotations.yaml")
	baseName = strings.TrimSuffix(baseName, ".warnings.txt")

	return baseName
}

func buildTestRequest(baseName string, filePaths []string, policyNames []string) *testRequest {
	matchedPolicyName := matchPolicyName(baseName, policyNames)
	expectAllowed := expectedAllowed(baseName)

	testReq := &testRequest{
		Name:          baseName + ".yaml",
		FilePath:      filePaths[0],
		PolicyName:    matchedPolicyName,
		ExpectAllowed: expectAllowed,
	}

	var hasExplicitRequest bool

	for _, filePath := range filePaths {
		if strings.HasSuffix(filePath, ".request.yaml") {
			hasExplicitRequest = true
		}

		tempReq := newTempTestRequest(filePath, matchedPolicyName, expectAllowed)

		if err := parseTestRequestFile(tempReq); err != nil {
			testReq.Error = fmt.Errorf("failed to parse test file %s: %w", filePath, err)

			return testReq
		}

		mergeTestRequests(testReq, tempReq)
	}

	if !hasExplicitRequest && testReq.Request != nil {
		op, err := InferOperation(testReq.Object != nil, testReq.OldObject != nil, "")
		if err == nil && op != "" {
			testReq.Request.Operation = admissionv1.Operation(op)
		}
	}

	return testReq
}

func matchPolicyName(baseName string, policyNames []string) string {
	for _, policyName := range policyNames {
		if strings.HasPrefix(baseName, policyName+".") {
			return policyName
		}
	}

	if len(policyNames) == 1 {
		return policyNames[0]
	}

	return ""
}

func expectedAllowed(baseName string) bool {
	if strings.Contains(baseName, ".deny.") || strings.HasSuffix(baseName, ".deny") {
		return false
	}

	if strings.Contains(baseName, ".audit.") || strings.Contains(baseName, ".warn.") {
		return true
	}

	// Default allow, explicitly captured allow suffixes
	return true
}

func newTempTestRequest(filePath, policyName string, expectAllowed bool) *testRequest {
	return &testRequest{
		Name:          filepath.Base(filePath),
		FilePath:      filePath,
		PolicyName:    policyName,
		ExpectAllowed: expectAllowed,
	}
}

//nolint:cyclop // Merge function with many fields
func mergeTestRequests(testReq, tempReq *testRequest) {
	if tempReq.Object != nil {
		testReq.Object = tempReq.Object
	}

	if tempReq.OldObject != nil {
		testReq.OldObject = tempReq.OldObject
	}

	if tempReq.Request != nil {
		mergeRequest(testReq, tempReq)
	}

	if tempReq.NamespaceObj != nil {
		testReq.NamespaceObj = tempReq.NamespaceObj
	}

	if tempReq.NamespaceName != "" {
		testReq.NamespaceName = tempReq.NamespaceName
	}

	if tempReq.UserInfo != nil {
		testReq.UserInfo = tempReq.UserInfo
	}

	if tempReq.Params != nil {
		testReq.Params = tempReq.Params
	}

	if tempReq.ExpectAuditAnnotations != nil {
		testReq.ExpectAuditAnnotations = tempReq.ExpectAuditAnnotations
	}

	if tempReq.ExpectedObject != nil {
		testReq.ExpectedObject = tempReq.ExpectedObject
	}

	if tempReq.ExpectMessage != "" {
		testReq.ExpectMessage = tempReq.ExpectMessage
	}

	if len(tempReq.ExpectWarnings) > 0 {
		testReq.ExpectWarnings = tempReq.ExpectWarnings
	}

	if tempReq.ExpectMutated {
		testReq.ExpectMutated = tempReq.ExpectMutated
	}
}

// mergeRequest merges fields from tempReq into testReq (tempReq takes precedence).
func mergeRequest(testReq, tempReq *testRequest) {
	if testReq.Request == nil {
		testReq.Request = tempReq.Request

		return
	}

	// Merge request fields (request.yaml takes precedence for metadata)
	if tempReq.Request.Operation != "" {
		testReq.Request.Operation = tempReq.Request.Operation
	}

	if tempReq.Request.SubResource != "" {
		testReq.Request.SubResource = tempReq.Request.SubResource
	}

	if tempReq.Request.Namespace != "" {
		testReq.Request.Namespace = tempReq.Request.Namespace
	}

	if tempReq.Request.Name != "" {
		testReq.Request.Name = tempReq.Request.Name
	}

	if tempReq.Request.UserInfo.Username != "" {
		testReq.Request.UserInfo = tempReq.Request.UserInfo
	}
	// Merge Options, Resource, Kind from the more detailed request
	if tempReq.Request.Options.Raw != nil {
		testReq.Request.Options = tempReq.Request.Options
	}

	if tempReq.Request.Resource.Resource != "" {
		testReq.Request.Resource = tempReq.Request.Resource
	}

	if tempReq.Request.Kind.Kind != "" {
		testReq.Request.Kind = tempReq.Request.Kind
	}
}

// hasPolicyFiles checks if a directory contains policy files.
func hasPolicyFiles(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("read dir %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Check for common policy file names
		if name == "policy.yaml" || name == "policy.yml" ||
			name == "binding.yaml" || name == "binding.yml" ||
			strings.HasSuffix(name, ".policy.yaml") || strings.HasSuffix(name, ".policy.yml") {
			return true, nil
		}
	}

	return false, nil
}
