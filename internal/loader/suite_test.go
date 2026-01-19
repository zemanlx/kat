package loader

import (
	"path/filepath"
	"testing"
)

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
			dir:            filepath.Join("..", "..", "test-policies-pass", "add-default-labels"),
			expectPolicies: true,
			expectTests:    true,
			minPolicies:    1,
			minBindings:    1,
		},
		{
			name:           "block-pod-exec",
			dir:            filepath.Join("..", "..", "test-policies-pass", "block-pod-exec"),
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
