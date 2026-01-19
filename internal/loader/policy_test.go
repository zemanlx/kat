package loader

import (
	"testing"
)

//nolint:gocognit,funlen,cyclop // Test table loop has many checks
func TestLoadPolicySet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		dir                    string
		wantMutatingPolicies   int
		wantMutatingBindings   int
		wantValidatingPolicies int
		wantValidatingBindings int
		wantPolicyNames        []string
		wantErr                bool
	}{
		{
			name:                   "single file with policy and binding",
			dir:                    "testdata/single-file-policy-binding",
			wantMutatingPolicies:   1,
			wantMutatingBindings:   1,
			wantValidatingPolicies: 0,
			wantValidatingBindings: 0,
			wantPolicyNames:        []string{"test-policy-1"},
		},
		{
			name:                   "multiple policies and bindings",
			dir:                    "testdata/multiple-policies",
			wantMutatingPolicies:   2,
			wantMutatingBindings:   2,
			wantValidatingPolicies: 0,
			wantValidatingBindings: 0,
			wantPolicyNames:        []string{"policy-a", "policy-b"},
		},
		{
			name:                   "mixed types",
			dir:                    "testdata/mixed-types",
			wantMutatingPolicies:   1,
			wantMutatingBindings:   1,
			wantValidatingPolicies: 1,
			wantValidatingBindings: 1,
			wantPolicyNames:        []string{"mutating-policy", "validating-policy"},
		},
		{
			name:                   "separate files",
			dir:                    "testdata/separate-files",
			wantMutatingPolicies:   1,
			wantMutatingBindings:   1,
			wantValidatingPolicies: 0,
			wantValidatingBindings: 0,
			wantPolicyNames:        []string{"separate-policy"},
		},
		{
			name:                   "validating only",
			dir:                    "testdata/validating-only",
			wantMutatingPolicies:   0,
			wantMutatingBindings:   0,
			wantValidatingPolicies: 1,
			wantValidatingBindings: 1,
			wantPolicyNames:        []string{"validating-only"},
		},
		{
			name:                   "real example sidecar-injection",
			dir:                    "../../test-policies-pass/sidecar-injection",
			wantMutatingPolicies:   1,
			wantMutatingBindings:   1,
			wantValidatingPolicies: 0,
			wantValidatingBindings: 0,
			wantPolicyNames:        []string{"sidecar-injection"},
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ps, err := LoadPolicySet(tc.dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadPolicySet() error = %v, wantErr %v", err, tc.wantErr)
			}

			if err != nil {
				return
			}

			if len(ps.MutatingPolicies) != tc.wantMutatingPolicies {
				t.Errorf("MutatingPolicies count = %d, want %d", len(ps.MutatingPolicies), tc.wantMutatingPolicies)
			}

			if len(ps.MutatingBindings) != tc.wantMutatingBindings {
				t.Errorf("MutatingBindings count = %d, want %d", len(ps.MutatingBindings), tc.wantMutatingBindings)
			}

			if len(ps.ValidatingPolicies) != tc.wantValidatingPolicies {
				t.Errorf("ValidatingPolicies count = %d, want %d", len(ps.ValidatingPolicies), tc.wantValidatingPolicies)
			}

			if len(ps.ValidatingBindings) != tc.wantValidatingBindings {
				t.Errorf("ValidatingBindings count = %d, want %d", len(ps.ValidatingBindings), tc.wantValidatingBindings)
			}

			// Check policy names if specified
			if len(tc.wantPolicyNames) > 0 {
				gotNames := make(map[string]bool)
				for _, p := range ps.MutatingPolicies {
					gotNames[p.Name] = true
				}

				for _, p := range ps.ValidatingPolicies {
					gotNames[p.Name] = true
				}

				for _, wantName := range tc.wantPolicyNames {
					if !gotNames[wantName] {
						t.Errorf("expected policy name %q not found, got %v", wantName, gotNames)
					}
				}
			}
		})
	}
}
