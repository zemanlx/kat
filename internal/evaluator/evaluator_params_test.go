package evaluator

import (
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	admissionv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// TestEvaluateMutating_WithParams tests mutating policies that use params.
//
//nolint:gocognit,funlen,cyclop,maintidx // Test function
func TestEvaluateMutating_WithParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		policy          *admissionv1beta1.MutatingAdmissionPolicy
		object          *unstructured.Unstructured
		params          *unstructured.Unstructured
		expectedMutated bool
		expectedObject  *unstructured.Unstructured
	}{
		{
			name: "add label from params",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "add", path: "/metadata/labels/env", value: params.environment}]`,
							},
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":   "test-pod",
						"labels": map[string]any{},
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"environment": "production",
				},
			},
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":   "test-pod",
						"labels": map[string]any{"env": "production"},
					},
				},
			},
		},
		{
			name: "conditional mutation based on params",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					MatchConditions: []admissionv1beta1.MatchCondition{
						{
							Name:       "check-enabled",
							Expression: `params.enabled == true`,
						},
					},
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "add", path: "/metadata/labels/managed", value: "true"}]`,
							},
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":   "test-pod",
						"labels": map[string]any{},
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"enabled": true,
				},
			},
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":   "test-pod",
						"labels": map[string]any{"managed": "true"},
					},
				},
			},
		},
		{
			name: "no mutation when params condition not met",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					MatchConditions: []admissionv1beta1.MatchCondition{
						{
							Name:       "check-enabled",
							Expression: `params.enabled == true`,
						},
					},
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "add", path: "/metadata/labels/managed", value: "true"}]`,
							},
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"enabled": false,
				},
			},
			expectedMutated: false,
		},
		{
			name: "add multiple labels from params object",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeApplyConfiguration,
							ApplyConfiguration: &admissionv1beta1.ApplyConfiguration{
								Expression: `Object{metadata: {"labels": params.defaultLabels}}`,
							},
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
						"labels": map[string]any{
							"app": "myapp",
						},
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"defaultLabels": map[string]any{
						"managed-by": "kat",
						"team":       "platform",
						"env":        "prod",
					},
				},
			},
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
						"labels": map[string]any{
							"app":        "myapp",
							"managed-by": "kat",
							"team":       "platform",
							"env":        "prod",
						},
					},
				},
			},
		},
		{
			name: "set replica count from params",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					MatchConditions: []admissionv1beta1.MatchCondition{
						{
							Name:       "check-apply-limit",
							Expression: `has(params.maxReplicas) && object.spec.replicas > params.maxReplicas`,
						},
					},
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeApplyConfiguration,
							ApplyConfiguration: &admissionv1beta1.ApplyConfiguration{
								Expression: `Object{spec: {"replicas": params.maxReplicas}}`,
							},
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name": "test-deployment",
					},
					"spec": map[string]any{
						"replicas": int64(100),
						"selector": map[string]any{
							"matchLabels": map[string]any{"app": "test"},
						},
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"maxReplicas": int64(10),
				},
			},
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name": "test-deployment",
					},
					"spec": map[string]any{
						"replicas": int64(10),
						"selector": map[string]any{
							"matchLabels": map[string]any{"app": "test"},
						},
					},
				},
			},
		},
		{
			name: "add annotation with value from nested params",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "add", path: "/metadata/annotations", value: {"owner": params.team.owner, "contact": params.team.contact}}]`,
							},
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"team": map[string]any{
						"owner":   "team-platform",
						"contact": "[email protected]",
					},
				},
			},
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
						"annotations": map[string]any{
							"owner":   "team-platform",
							"contact": "[email protected]",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			evaluator, err := New()
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			request := &admissionv1.AdmissionRequest{
				UID:       types.UID("test-uid"),
				Name:      "test-object",
				Namespace: "default",
				Operation: admissionv1.Create,
			}

			result, err := evaluator.EvaluateMutating(tc.policy, request, tc.object, nil, tc.params, nil, nil, nil)
			if err != nil {
				t.Fatalf("EvaluateMutating() error = %v", err)
			}

			if !result.Allowed {
				t.Errorf("EvaluateMutating() Allowed = false, want true")
			}

			if !tc.expectedMutated {
				if result.PatchedObject != nil {
					t.Error("EvaluateMutating() should not return patched object when no mutation applied")
				}

				return
			}

			if result.PatchedObject == nil {
				t.Fatal("EvaluateMutating() should return patched object")
			}

			if tc.expectedObject == nil {
				return
			}

			gotJSON, err := json.MarshalIndent(result.PatchedObject.Object, "", "  ")
			if err != nil {
				t.Fatalf("Failed to marshal patched object: %v", err)
			}

			wantJSON, err := json.MarshalIndent(tc.expectedObject.Object, "", "  ")
			if err != nil {
				t.Fatalf("Failed to marshal expected object: %v", err)
			}

			if string(gotJSON) != string(wantJSON) {
				t.Errorf("Patched object differs from expected.\n\nGot:\n%s\n\nWant:\n%s", string(gotJSON), string(wantJSON))
			}
		})
	}
}

// TestEvaluateValidating_WithParams tests validating policies that use params.
//
//nolint:funlen,maintidx // Test function
func TestEvaluateValidating_WithParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		policy        *admissionregv1.ValidatingAdmissionPolicy
		object        *unstructured.Unstructured
		params        *unstructured.Unstructured
		expectAllowed bool
		expectMessage string
	}{
		{
			name: "allow when label matches params requirement",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `has(object.metadata.labels) && has(object.metadata.labels.env) && object.metadata.labels.env in params.allowedEnvironments`,
							Message:    "Environment must be in allowed list",
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
						"labels": map[string]any{
							"env": "production",
						},
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"allowedEnvironments": []any{"development", "staging", "production"},
				},
			},
			expectAllowed: true,
		},
		{
			name: "deny when label not in params allowed list",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `has(object.metadata.labels) && has(object.metadata.labels.env) && object.metadata.labels.env in params.allowedEnvironments`,
							Message:    "Environment must be in allowed list",
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
						"labels": map[string]any{
							"env": "experimental",
						},
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"allowedEnvironments": []any{"development", "staging", "production"},
				},
			},
			expectAllowed: false,
			expectMessage: "Environment must be in allowed list",
		},
		{
			name: "enforce replica limit from params",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `object.spec.replicas <= params.maxReplicas`,
							Message:    "Replica count exceeds maximum allowed",
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name": "test-deployment",
					},
					"spec": map[string]any{
						"replicas": int64(5),
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"maxReplicas": int64(10),
				},
			},
			expectAllowed: true,
		},
		{
			name: "deny when replica limit exceeded",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `object.spec.replicas <= params.maxReplicas`,
							Message:    "Replica count exceeds maximum allowed",
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name": "test-deployment",
					},
					"spec": map[string]any{
						"replicas": int64(50),
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"maxReplicas": int64(10),
				},
			},
			expectAllowed: false,
			expectMessage: "Replica count exceeds maximum allowed",
		},
		{
			name: "validate required labels from params",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `has(object.metadata.labels) && params.requiredLabels.all(label, label in object.metadata.labels)`,
							Message:    "Required labels are missing",
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
						"labels": map[string]any{
							"app":   "myapp",
							"owner": "team-a",
							"env":   "production",
						},
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"requiredLabels": []any{"app", "owner", "env"},
				},
			},
			expectAllowed: true,
		},
		{
			name: "deny when required labels missing",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `has(object.metadata.labels) && params.requiredLabels.all(label, label in object.metadata.labels)`,
							Message:    "Required labels are missing",
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
						"labels": map[string]any{
							"app": "myapp",
						},
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"requiredLabels": []any{"app", "owner", "env"},
				},
			},
			expectAllowed: false,
			expectMessage: "Required labels are missing",
		},
		{
			name: "match condition using params - policy not evaluated",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					MatchConditions: []admissionregv1.MatchCondition{
						{
							Name:       "check-enforcement",
							Expression: `params.enforcePolicy == true`,
						},
					},
					Validations: []admissionregv1.Validation{
						{
							Expression: `false`, // This would fail if evaluated
							Message:    "Should not be evaluated",
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"enforcePolicy": false,
				},
			},
			expectAllowed: true,
		},
		{
			name: "complex nested params validation",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `has(object.metadata.labels.team) && object.metadata.labels.team in params.teams && object.spec.replicas <= params.limits[object.metadata.labels.team].maxReplicas`,
							Message:    "Team not authorized or replica limit exceeded for team",
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name": "test-deployment",
						"labels": map[string]any{
							"team": "platform",
						},
					},
					"spec": map[string]any{
						"replicas": int64(5),
					},
				},
			},
			params: &unstructured.Unstructured{
				Object: map[string]any{
					"teams": []any{"platform", "backend", "frontend"},
					"limits": map[string]any{
						"platform": map[string]any{
							"maxReplicas": int64(10),
						},
						"backend": map[string]any{
							"maxReplicas": int64(5),
						},
						"frontend": map[string]any{
							"maxReplicas": int64(3),
						},
					},
				},
			},
			expectAllowed: true,
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			evaluator, err := New()
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			request := &admissionv1.AdmissionRequest{
				UID:       types.UID("test-uid"),
				Name:      "test-object",
				Namespace: "default",
				Operation: admissionv1.Create,
			}

			result, err := evaluator.EvaluateValidating(tc.policy, nil, request, tc.object, nil, tc.params, nil, nil, nil)
			if err != nil {
				t.Fatalf("EvaluateValidating() error = %v", err)
			}

			if result.Allowed != tc.expectAllowed {
				t.Errorf("EvaluateValidating() Allowed = %v, want %v", result.Allowed, tc.expectAllowed)
			}

			if tc.expectMessage != "" && result.Message != tc.expectMessage {
				t.Errorf("EvaluateValidating() Message = %q, want %q", result.Message, tc.expectMessage)
			}
		})
	}
}
