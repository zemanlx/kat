package evaluator

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	admissionv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/authentication/user"
)

func TestNew(t *testing.T) {
	t.Parallel()

	evaluator, err := New()
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	if evaluator == nil {
		t.Fatal("New() returned nil evaluator")
	}

	if evaluator.env == nil {
		t.Fatal("New() evaluator has nil env")
	}
}

//nolint:gocognit,funlen,cyclop,maintidx // Test function
func TestEvaluateMutating(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		policy          *admissionv1beta1.MutatingAdmissionPolicy
		object          *unstructured.Unstructured
		oldObject       *unstructured.Unstructured
		expectedMutated bool // true if object should be mutated
		expectedObject  *unstructured.Unstructured
		expectedError   bool
	}{
		{
			name: "add label when match conditions satisfied",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					MatchConditions: []admissionv1beta1.MatchCondition{
						{
							Name:       "check-namespace",
							Expression: `object.metadata.namespace == "default"`,
						},
					},
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "add", path: "/metadata/labels/matched", value: "true"}]`,
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
						"name":      "test-pod",
						"namespace": "default",
						"labels":    map[string]any{},
					},
				},
			},
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":      "test-pod",
						"namespace": "default",
						"labels":    map[string]any{"matched": "true"},
					},
				},
			},
		},
		{
			name: "no mutation when match conditions not satisfied",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					MatchConditions: []admissionv1beta1.MatchCondition{
						{
							Name:       "check-namespace",
							Expression: `object.metadata.namespace == "production"`,
						},
					},
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "add", path: "/metadata/labels/matched", value: "true"}]`,
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
						"name":      "test-pod",
						"namespace": "default",
					},
				},
			},
			expectedMutated: false,
		},
		{
			name: "add multiple labels",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[
									JSONPatch{op: "add", path: "/metadata/labels/env", value: "test"},
									JSONPatch{op: "add", path: "/metadata/labels/team", value: "platform"}
								]`,
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
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":   "test-pod",
						"labels": map[string]any{"env": "test", "team": "platform"},
					},
				},
			},
		},
		{
			name: "add audit label when replica count increased",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					MatchConditions: []admissionv1beta1.MatchCondition{
						{
							Name:       "check-replica-increase",
							Expression: `has(oldObject.spec.replicas) && has(object.spec.replicas) && object.spec.replicas > oldObject.spec.replicas`,
						},
					},
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "add", path: "/metadata/labels/scaled-up", value: "true"}]`,
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
						"name":   "test-deployment",
						"labels": map[string]any{},
					},
					"spec": map[string]any{
						"replicas": float64(5),
					},
				},
			},
			oldObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":   "test-deployment",
						"labels": map[string]any{},
					},
					"spec": map[string]any{
						"replicas": float64(3),
					},
				},
			},
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":   "test-deployment",
						"labels": map[string]any{"scaled-up": "true"},
					},
					"spec": map[string]any{
						"replicas": float64(5),
					},
				},
			},
		},
		{
			name: "apply configuration - merge spec fields",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeApplyConfiguration,
							ApplyConfiguration: &admissionv1beta1.ApplyConfiguration{
								Expression: `Object{spec: {"replicas": 10}}`,
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
						"replicas": float64(3),
						"selector": map[string]any{
							"matchLabels": map[string]any{
								"app": "test",
							},
						},
					},
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
							"matchLabels": map[string]any{
								"app": "test",
							},
						},
					},
				},
			},
		},
		{
			name: "mixed patch types - preserve spec order",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "add", path: "/metadata/annotations/step", value: "1"}]`,
							},
						},
						{
							PatchType: admissionv1beta1.PatchTypeApplyConfiguration,
							ApplyConfiguration: &admissionv1beta1.ApplyConfiguration{
								Expression: `Object{metadata: Object.metadata{annotations: {"step": "2"}}}`,
							},
						},
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "replace", path: "/metadata/annotations/step", value: "3"}]`,
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
						"name":        "test-pod",
						"annotations": map[string]any{},
					},
				},
			},
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":        "test-pod",
						"annotations": map[string]any{"step": "3"},
					},
				},
			},
		},
		{
			name: "apply configuration - add nested labels",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeApplyConfiguration,
							ApplyConfiguration: &admissionv1beta1.ApplyConfiguration{
								Expression: `Object{metadata: {"labels": {"managed-by": "kat", "env": "prod"}}}`,
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
							"env":        "prod",
						},
					},
				},
			},
		},
		{
			name: "json patch - complex nested object value",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								// Using Object.spec.selector{} style nested object
								Expression: `[JSONPatch{op: "add", path: "/spec/selector", value: Object.spec.selector{matchLabels: {"app": "myapp", "env": "prod"}}}]`,
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
						"replicas": float64(3),
					},
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
						"replicas": float64(3),
						"selector": map[string]any{
							"matchLabels": map[string]any{
								"app": "myapp",
								"env": "prod",
							},
						},
					},
				},
			},
		},
		{
			name: "json patch - array with complex objects",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								// Adding a complex container with nested env vars
								Expression: `[JSONPatch{op: "add", path: "/spec/containers", value: [{"name": "nginx", "image": "nginx:latest", "env": [{"name": "ENV", "value": "prod"}], "ports": [{"containerPort": 80}]}]}]`,
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
					"spec": map[string]any{},
				},
			},
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
					},
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  "nginx",
								"image": "nginx:latest",
								"env": []any{
									map[string]any{
										"name":  "ENV",
										"value": "prod",
									},
								},
								"ports": []any{
									map[string]any{
										"containerPort": float64(80),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "json patch - nested map value",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								// Adding deeply nested configuration
								Expression: `[JSONPatch{op: "add", path: "/metadata/annotations", value: {"config.example.com/nested": "{\"key1\": \"value1\", \"key2\": {\"nested\": true}}"}}]`,
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
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
						"annotations": map[string]any{
							"config.example.com/nested": `{"key1": "value1", "key2": {"nested": true}}`,
						},
					},
				},
			},
		},
		{
			name: "apply configuration - deeply nested structure",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeApplyConfiguration,
							ApplyConfiguration: &admissionv1beta1.ApplyConfiguration{
								// Complex nested structure with arrays and objects
								Expression: `Object{spec: Object.spec{template: Object.spec.template{spec: Object.spec.template.spec{containers: [{"name": "sidecar", "image": "sidecar:v1", "env": [{"name": "MODE", "value": "inject"}]}]}}}}`,
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
						"replicas": float64(3),
						"template": map[string]any{
							"metadata": map[string]any{
								"labels": map[string]any{"app": "test"},
							},
							"spec": map[string]any{
								"containers": []any{
									map[string]any{
										"name":  "main",
										"image": "main:v1",
									},
								},
							},
						},
					},
				},
			},
			expectedMutated: true,
		},
		{
			name: "apply configuration - merge arrays and objects",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeApplyConfiguration,
							ApplyConfiguration: &admissionv1beta1.ApplyConfiguration{
								// Add volumes array with complex nested structure
								Expression: `Object{spec: {"volumes": [{"name": "config", "configMap": {"name": "app-config", "items": [{"key": "config.yaml", "path": "config.yaml"}]}}]}}`,
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
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  "app",
								"image": "app:v1",
							},
						},
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
					},
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  "app",
								"image": "app:v1",
							},
						},
						"volumes": []any{
							map[string]any{
								"name": "config",
								"configMap": map[string]any{
									"name": "app-config",
									"items": []any{
										map[string]any{
											"key":  "config.yaml",
											"path": "config.yaml",
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "json patch - multiple patches with complex values",
			policy: &admissionv1beta1.MutatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								// Multiple patches in one mutation with nested values
								Expression: `[
									JSONPatch{op: "add", path: "/metadata/labels", value: {"tier": "backend", "version": "v1"}},
									JSONPatch{op: "add", path: "/spec/strategy", value: {"type": "RollingUpdate", "rollingUpdate": {"maxSurge": "25%", "maxUnavailable": 0}}}
								]`,
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
						"replicas": float64(3),
					},
				},
			},
			expectedMutated: true,
			expectedObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name": "test-deployment",
						"labels": map[string]any{
							"tier":    "backend",
							"version": "v1",
						},
					},
					"spec": map[string]any{
						"replicas": float64(3),
						"strategy": map[string]any{
							"type": "RollingUpdate",
							"rollingUpdate": map[string]any{
								"maxSurge":       "25%",
								"maxUnavailable": float64(0),
							},
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
				Name:      "test-pod",
				Namespace: "default",
				Operation: admissionv1.Create,
			}

			result, err := evaluator.EvaluateMutating(tc.policy, request, tc.object, tc.oldObject, nil, nil, nil, nil)

			if tc.expectedError {
				if err == nil {
					t.Fatalf("EvaluateMutating() expected error but got none")
				}

				return
			}

			if err != nil {
				t.Fatalf("EvaluateMutating() error = %v, want nil", err)
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

			// Use cmp.Diff for clear comparison, showing only differences
			if diff := cmp.Diff(tc.expectedObject.Object, result.PatchedObject.Object); diff != "" {
				t.Errorf("Patched object mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

//nolint:gocognit,funlen,cyclop // Test function
func TestEvaluateValidating(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		policy         *admissionregv1.ValidatingAdmissionPolicy
		object         *unstructured.Unstructured
		oldObject      *unstructured.Unstructured
		expectAllowed  bool
		expectMessage  string
		expectWarnings []string
	}{
		{
			name: "allow when validation passes",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `object.metadata.name.startsWith("test-")`,
							Message:    "Pod name must start with test-",
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
			expectAllowed: true,
		},
		{
			name: "deny when validation fails",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `object.metadata.name.startsWith("test-")`,
							Message:    "Pod name must start with test-",
						},
					},
				},
			},
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "bad-pod",
					},
				},
			},
			expectAllowed: false,
			expectMessage: "Pod name must start with test-",
		},
		{
			name: "allow when match conditions fail - policy not evaluated",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					MatchConditions: []admissionregv1.MatchCondition{
						{
							Name:       "check-namespace",
							Expression: `object.metadata.namespace == "production"`,
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
						"name":      "test-pod",
						"namespace": "default",
					},
				},
			},
			expectAllowed: true,
		},
		{
			name: "deny when owner label is changed",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "prevent-owner-change"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `!has(oldObject.metadata.labels) || !has(oldObject.metadata.labels.owner) || !has(object.metadata.labels) || !has(object.metadata.labels.owner) || oldObject.metadata.labels.owner == object.metadata.labels.owner`,
							Message:    "Owner label cannot be changed",
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
							"owner": "team-b",
						},
					},
				},
			},
			oldObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
						"labels": map[string]any{
							"owner": "team-a",
						},
					},
				},
			},
			expectAllowed: false,
			expectMessage: "Owner label cannot be changed",
		},
		{
			name: "allow when owner label unchanged",
			policy: &admissionregv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "prevent-owner-change"},
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{
							Expression: `!has(oldObject.metadata.labels) || !has(oldObject.metadata.labels.owner) || !has(object.metadata.labels) || !has(object.metadata.labels.owner) || oldObject.metadata.labels.owner == object.metadata.labels.owner`,
							Message:    "Owner label cannot be changed",
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
							"owner": "team-a",
							"env":   "production", // Other labels can change
						},
					},
				},
			},
			oldObject: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name": "test-pod",
						"labels": map[string]any{
							"owner": "team-a",
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
				Name:      "test-pod",
				Namespace: "default",
				Operation: admissionv1.Create,
			}

			result, err := evaluator.EvaluateValidating(tc.policy, nil, request, tc.object, tc.oldObject, nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("EvaluateValidating() error = %v", err)
			}

			if result.Allowed != tc.expectAllowed {
				t.Errorf("EvaluateValidating() Allowed = %v, want %v", result.Allowed, tc.expectAllowed)
			}

			if tc.expectMessage != "" && result.Message != tc.expectMessage {
				t.Errorf("EvaluateValidating() Message = %q, want %q", result.Message, tc.expectMessage)
			}

			if len(tc.expectWarnings) > 0 {
				if len(result.Warnings) != len(tc.expectWarnings) {
					t.Errorf("EvaluateValidating() Warnings count = %d, want %d", len(result.Warnings), len(tc.expectWarnings))
				}

				for i, expectedWarning := range tc.expectWarnings {
					if i < len(result.Warnings) && result.Warnings[i] != expectedWarning {
						t.Errorf("EvaluateValidating() Warning[%d] = %q, want %q", i, result.Warnings[i], expectedWarning)
					}
				}
			}
		})
	}
}

func TestEvaluateValidating_MultipleValidations(t *testing.T) {
	t.Parallel()

	evaluator, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	policy := &admissionregv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-policy",
		},
		Spec: admissionregv1.ValidatingAdmissionPolicySpec{
			Validations: []admissionregv1.Validation{
				{
					Expression: `object.metadata.name.startsWith("test-")`,
					Message:    "Pod name must start with test-",
				},
				{
					Expression: `has(object.metadata.labels) && has(object.metadata.labels.environment)`,
					Message:    "Pod must have environment label",
				},
			},
		},
	}

	object := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name": "test-pod",
				// Missing labels - second validation should fail
			},
		},
	}

	request := &admissionv1.AdmissionRequest{
		UID:       types.UID("test-uid"),
		Name:      "test-pod",
		Operation: admissionv1.Create,
	}

	result, err := evaluator.EvaluateValidating(policy, nil, request, object, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateValidating() error = %v", err)
	}

	if result.Allowed {
		t.Errorf("EvaluateValidating() Allowed = true, want false")
	}

	expectedMessage := "Pod must have environment label"
	if result.Message != expectedMessage {
		t.Errorf("EvaluateValidating() Message = %q, want %q", result.Message, expectedMessage)
	}
}

//nolint:funlen // Test function
func TestEvaluateExpression_Simple(t *testing.T) {
	t.Parallel()

	evaluator, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	tests := []struct {
		name       string
		expression string
		vars       map[string]any
		want       any
		wantErr    bool
	}{
		{
			name:       "simple boolean true",
			expression: "true",
			vars:       map[string]any{},
			want:       true,
			wantErr:    false,
		},
		{
			name:       "simple boolean false",
			expression: "false",
			vars:       map[string]any{},
			want:       false,
			wantErr:    false,
		},
		{
			name:       "string comparison",
			expression: `object.name == "test"`,
			vars:       map[string]any{"object": map[string]any{"name": "test"}},
			want:       true,
			wantErr:    false,
		},
		{
			name:       "has() function",
			expression: `has(object.metadata)`,
			vars:       map[string]any{"object": map[string]any{"metadata": map[string]any{}}},
			want:       true,
			wantErr:    false,
		},
		{
			name:       "invalid expression",
			expression: "this is not valid CEL",
			vars:       map[string]any{},
			want:       nil,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := evaluator.evaluateExpression(tc.expression, tc.vars)
			if (err != nil) != tc.wantErr {
				t.Errorf("evaluateExpression() error = %v, wantErr %v", err, tc.wantErr)

				return
			}

			if !tc.wantErr && got != tc.want {
				t.Errorf("evaluateExpression() = %v, want %v", got, tc.want)
			}
		})
	}
}

//nolint:cyclop // Covers many admission request shapes and fields
func TestConvertAdmissionRequest(t *testing.T) {
	t.Parallel()

	dryRun := true
	request := &admissionv1.AdmissionRequest{
		UID:       types.UID("test-uid"),
		Kind:      metav1.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Resource:  metav1.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		Name:      "test-deployment",
		Namespace: "default",
		Operation: admissionv1.Create,
		UserInfo: authenticationv1.UserInfo{
			Username: "test-user",
			UID:      "test-uid",
			Groups:   []string{"system:authenticated", "developers"},
		},
		DryRun: &dryRun,
	}

	result, err := convertAdmissionRequest(request)
	if err != nil {
		t.Fatalf("convertAdmissionRequest() error = %v", err)
	}

	// runtime.DefaultUnstructuredConverter converts types to their basic equivalents
	// types.UID becomes string, etc.
	if result["uid"] != "test-uid" {
		t.Errorf("convertAdmissionRequest() uid = %v, want test-uid", result["uid"])
	}

	if result["name"] != "test-deployment" {
		t.Errorf("convertAdmissionRequest() name = %v, want test-deployment", result["name"])
	}

	if result["namespace"] != "default" {
		t.Errorf("convertAdmissionRequest() namespace = %v, want default", result["namespace"])
	}

	if result["operation"] != "CREATE" {
		t.Errorf("convertAdmissionRequest() operation = %v, want CREATE", result["operation"])
	}

	if result["dryRun"] != true {
		t.Errorf("convertAdmissionRequest() dryRun = %v, want true", result["dryRun"])
	}

	// Check kind structure
	kind, ok := result["kind"].(map[string]any)
	if !ok {
		t.Fatal("convertAdmissionRequest() kind is not a map")
	}

	if kind["group"] != "apps" || kind["version"] != "v1" || kind["kind"] != "Deployment" {
		t.Errorf("convertAdmissionRequest() kind = %v, want {group:apps, version:v1, kind:Deployment}", kind)
	}

	// Check userInfo structure
	userInfo, ok := result["userInfo"].(map[string]any)
	if !ok {
		t.Fatal("convertAdmissionRequest() userInfo is not a map")
	}

	if userInfo["username"] != "test-user" {
		t.Errorf("convertAdmissionRequest() userInfo.username = %v, want test-user", userInfo["username"])
	}
}

//nolint:funlen // Test function
func TestEvaluateMatchConditions(t *testing.T) {
	t.Parallel()

	evaluator, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	tests := []struct {
		name       string
		conditions []admissionv1beta1.MatchCondition
		vars       map[string]any
		want       bool
		wantErr    bool
	}{
		{
			name:       "no conditions - should match",
			conditions: []admissionv1beta1.MatchCondition{},
			vars:       map[string]any{},
			want:       true,
			wantErr:    false,
		},
		{
			name: "single condition - match",
			conditions: []admissionv1beta1.MatchCondition{
				{Name: "test", Expression: "true"},
			},
			vars:    map[string]any{},
			want:    true,
			wantErr: false,
		},
		{
			name: "single condition - no match",
			conditions: []admissionv1beta1.MatchCondition{
				{Name: "test", Expression: "false"},
			},
			vars:    map[string]any{},
			want:    false,
			wantErr: false,
		},
		{
			name: "multiple conditions - all match",
			conditions: []admissionv1beta1.MatchCondition{
				{Name: "test1", Expression: "true"},
				{Name: "test2", Expression: "true"},
			},
			vars:    map[string]any{},
			want:    true,
			wantErr: false,
		},
		{
			name: "multiple conditions - first fails",
			conditions: []admissionv1beta1.MatchCondition{
				{Name: "test1", Expression: "false"},
				{Name: "test2", Expression: "true"},
			},
			vars:    map[string]any{},
			want:    false,
			wantErr: false,
		},
		{
			name: "condition with variables",
			conditions: []admissionv1beta1.MatchCondition{
				{Name: "test", Expression: `object.namespace == "default"`},
			},
			vars:    map[string]any{"object": map[string]any{"namespace": "default"}},
			want:    true,
			wantErr: false,
		},
		{
			name: "invalid expression",
			conditions: []admissionv1beta1.MatchCondition{
				{Name: "test", Expression: "not valid cel"},
			},
			vars:    map[string]any{},
			want:    false,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := evaluator.evaluateMatchConditionsV1Beta1(tc.conditions, tc.vars)
			if (err != nil) != tc.wantErr {
				t.Errorf("evaluateMatchConditions() error = %v, wantErr %v", err, tc.wantErr)

				return
			}

			if got != tc.want {
				t.Errorf("evaluateMatchConditions() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRealPolicy_RequireOwnerLabel tests the require-owner-label policy from test-policies-pass.
//
//nolint:funlen // Test function
func TestRealPolicy_RequireOwnerLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		object        *unstructured.Unstructured
		expectAllowed bool
		expectMessage string
	}{
		{
			name: "with owner label - should allow",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":      "test-deployment",
						"namespace": "default",
						"labels": map[string]any{
							"owner": "team-a",
						},
					},
				},
			},
			expectAllowed: true,
		},
		{
			name: "without owner label - should deny",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":      "test-deployment",
						"namespace": "default",
						"labels": map[string]any{
							"app": "myapp",
						},
					},
				},
			},
			expectAllowed: false,
			expectMessage: "All workloads must have an 'owner' label",
		},
		{
			name: "without labels - should deny",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":      "test-deployment",
						"namespace": "default",
					},
				},
			},
			expectAllowed: false,
		},
	}

	policy := &admissionregv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "require-owner-label",
		},
		Spec: admissionregv1.ValidatingAdmissionPolicySpec{
			Validations: []admissionregv1.Validation{
				{
					Expression: "has(object.metadata.labels) && 'owner' in object.metadata.labels",
					Message:    "All workloads must have an 'owner' label",
				},
			},
		},
	}

	request := &admissionv1.AdmissionRequest{
		UID:       types.UID("test-uid"),
		Name:      "test-deployment",
		Namespace: "default",
		Operation: admissionv1.Create,
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			evaluator, err := New()
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			result, err := evaluator.EvaluateValidating(policy, nil, request, tc.object, nil, nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("EvaluateValidating() error = %v", err)
			}

			if result.Allowed != tc.expectAllowed {
				t.Errorf("Expected allowed=%v, got %v. Message: %s", tc.expectAllowed, result.Allowed, result.Message)
			}

			if tc.expectMessage != "" && result.Message != tc.expectMessage {
				t.Errorf("Expected message %q, got %q", tc.expectMessage, result.Message)
			}
		})
	}
}

// TestRealPolicy_BlockPrivilegedContainers tests the block-privileged-containers policy.
//
//nolint:funlen // Test function
func TestRealPolicy_BlockPrivilegedContainers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		object        *unstructured.Unstructured
		expectAllowed bool
		expectMessage string
	}{
		{
			name: "unprivileged pod - should allow",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":      "unprivileged-pod",
						"namespace": "default",
					},
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  "nginx",
								"image": "nginx",
								"securityContext": map[string]any{
									"privileged": false,
								},
							},
						},
					},
				},
			},
			expectAllowed: true,
		},
		{
			name: "privileged pod - should deny",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":      "privileged-pod",
						"namespace": "default",
					},
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  "nginx",
								"image": "nginx",
								"securityContext": map[string]any{
									"privileged": true,
								},
							},
						},
					},
				},
			},
			expectAllowed: false,
			expectMessage: "Privileged containers are not allowed",
		},
		{
			name: "pod without securityContext - should allow",
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]any{
						"name":      "normal-pod",
						"namespace": "default",
					},
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  "nginx",
								"image": "nginx",
							},
						},
					},
				},
			},
			expectAllowed: true,
		},
	}

	policy := &admissionregv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "block-privileged-containers",
		},
		Spec: admissionregv1.ValidatingAdmissionPolicySpec{
			Validations: []admissionregv1.Validation{
				{
					Expression: `!has(object.spec.containers) ||
object.spec.containers.all(container,
  !has(container.securityContext) ||
  !has(container.securityContext.privileged) ||
  container.securityContext.privileged == false
)`,
					Message: "Privileged containers are not allowed",
				},
			},
		},
	}

	request := &admissionv1.AdmissionRequest{
		UID:       types.UID("test-uid"),
		Name:      "test-pod",
		Namespace: "default",
		Operation: admissionv1.Create,
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			evaluator, err := New()
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			result, err := evaluator.EvaluateValidating(policy, nil, request, tc.object, nil, nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("EvaluateValidating() error = %v", err)
			}

			if result.Allowed != tc.expectAllowed {
				t.Errorf("Expected allowed=%v, got %v. Message: %s", tc.expectAllowed, result.Allowed, result.Message)
			}

			if tc.expectMessage != "" && result.Message != tc.expectMessage {
				t.Errorf("Expected message %q, got %q", tc.expectMessage, result.Message)
			}
		})
	}
}

type MockTestCase struct {
	Request                *admissionv1.AdmissionRequest
	Object                 *unstructured.Unstructured
	OldObject              *unstructured.Unstructured
	Params                 *unstructured.Unstructured
	NamespaceObj           *unstructured.Unstructured
	UserInfo               user.Info
	ExpectAllowed          bool
	ExpectMessage          string
	ExpectWarnings         []string
	ExpectAuditAnnotations map[string]string
	ExpectedObject         *unstructured.Unstructured
	Error                  error
	Authorizer             []AuthorizationMockConfig
}

func (m MockTestCase) GetRequest() *admissionv1.AdmissionRequest     { return m.Request }
func (m MockTestCase) GetObject() *unstructured.Unstructured         { return m.Object }
func (m MockTestCase) GetOldObject() *unstructured.Unstructured      { return m.OldObject }
func (m MockTestCase) GetParams() *unstructured.Unstructured         { return m.Params }
func (m MockTestCase) GetNamespaceObj() *unstructured.Unstructured   { return m.NamespaceObj }
func (m MockTestCase) GetUserInfo() user.Info                        { return m.UserInfo }
func (m MockTestCase) GetExpectAllowed() bool                        { return m.ExpectAllowed }
func (m MockTestCase) GetExpectMessage() string                      { return m.ExpectMessage }
func (m MockTestCase) GetExpectWarnings() []string                   { return m.ExpectWarnings }
func (m MockTestCase) GetExpectAuditAnnotations() map[string]string  { return m.ExpectAuditAnnotations }
func (m MockTestCase) GetExpectedObject() *unstructured.Unstructured { return m.ExpectedObject }
func (m MockTestCase) GetError() error                               { return m.Error }
func (m MockTestCase) GetAuthorizer() []AuthorizationMockConfig      { return m.Authorizer }

//nolint:funlen,maintidx // Test function
func TestEvaluator_EvaluateTest(t *testing.T) {
	t.Parallel()

	evaluator, err := New()
	if err != nil {
		t.Fatalf("Failed to create evaluator: %v", err)
	}

	validPod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]interface{}{
				"name":      "test-pod",
				"namespace": "default",
			},
		},
	}

	tests := []struct {
		name              string
		mutatingPolicy    *admissionv1beta1.MutatingAdmissionPolicy
		validatingPolicy  *admissionregv1.ValidatingAdmissionPolicy
		validatingBinding *admissionregv1.ValidatingAdmissionPolicyBinding
		testCase          MockTestCase
		wantPassed        bool
		wantMessage       string
	}{
		{
			name: "TestCase with Error",
			testCase: MockTestCase{
				//nolint:err113 // Dynamic error for test
				Error: errors.New("loading error"),
			},
			wantPassed:  false,
			wantMessage: "test loading error: loading error",
		},
		{
			name: "No Policy Provided",
			testCase: MockTestCase{
				Object: validPod,
			},
			wantPassed:  false,
			wantMessage: "no policy provided",
		},
		{
			name: "Validating Policy Pass",
			validatingPolicy: &admissionregv1.ValidatingAdmissionPolicy{
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{Expression: "true"},
					},
				},
			},
			testCase: MockTestCase{
				Object:        validPod,
				ExpectAllowed: true,
			},
			wantPassed: true,
		},
		{
			name: "Validating Policy Fail - Mismatch Expected Allowed",
			validatingPolicy: &admissionregv1.ValidatingAdmissionPolicy{
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{Expression: "false", Message: "denied"},
					},
				},
			},
			testCase: MockTestCase{
				Object:        validPod,
				ExpectAllowed: true, // Expect allow, but policy denies
			},
			wantPassed:  false,
			wantMessage: "expected allowed=true, got allowed=false",
		},
		{
			name: "Validating Policy Fail - Correct Expectation",
			validatingPolicy: &admissionregv1.ValidatingAdmissionPolicy{
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{Expression: "false", Message: "denied"},
					},
				},
			},
			testCase: MockTestCase{
				Object:        validPod,
				ExpectAllowed: false,
				ExpectMessage: "denied",
			},
			wantPassed: true,
		},
		{
			name: "Mutating Policy Success",
			mutatingPolicy: &admissionv1beta1.MutatingAdmissionPolicy{
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "add", path: "/metadata/labels", value: {"foo": "bar"}}]`,
							},
						},
					},
				},
			},
			testCase: MockTestCase{
				Object:        validPod,
				ExpectAllowed: true,
				ExpectedObject: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Pod",
						"metadata": map[string]interface{}{
							"name":      "test-pod",
							"namespace": "default",
							"labels": map[string]interface{}{
								"foo": "bar",
							},
						},
					},
				},
			},
			wantPassed: true,
		},
		{
			name: "Mutating Policy - Expected Object Mismatch",
			mutatingPolicy: &admissionv1beta1.MutatingAdmissionPolicy{
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `[JSONPatch{op: "add", path: "/metadata/labels", value: {"foo": "bar"}}]`,
							},
						},
					},
				},
			},
			testCase: MockTestCase{
				Object:        validPod,
				ExpectAllowed: true,
				ExpectedObject: &unstructured.Unstructured{ // Expect different label
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Pod",
						"metadata": map[string]interface{}{
							"name":      "test-pod",
							"namespace": "default",
							"labels": map[string]interface{}{
								"foo": "baz",
							},
						},
					},
				},
			},
			wantPassed:  false,
			wantMessage: "mutated object does not match expected",
		},
		{
			name: "Audit Annotation Mismatch",
			validatingPolicy: &admissionregv1.ValidatingAdmissionPolicy{
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					AuditAnnotations: []admissionregv1.AuditAnnotation{
						{Key: "key1", ValueExpression: "'value1'"},
					},
					Validations: []admissionregv1.Validation{{Expression: "true"}},
				},
			},
			testCase: MockTestCase{
				Object: validPod,
				ExpectAuditAnnotations: map[string]string{
					"key1": "value2", // Mismatch expectation
				},
				ExpectAllowed: true,
			},
			wantPassed:  false,
			wantMessage: "audit annotations do not match expected",
		},
		{
			name: "Warning Mismatch",
			validatingPolicy: &admissionregv1.ValidatingAdmissionPolicy{
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{Expression: "false", Message: "warn me"},
					},
				},
			},
			validatingBinding: &admissionregv1.ValidatingAdmissionPolicyBinding{
				Spec: admissionregv1.ValidatingAdmissionPolicyBindingSpec{
					ValidationActions: []admissionregv1.ValidationAction{admissionregv1.Warn},
				},
			},
			testCase: MockTestCase{
				Object:         validPod,
				ExpectAllowed:  true,
				ExpectWarnings: []string{"expecting different warning"},
			},
			wantPassed:  false,
			wantMessage: "warning[0] does not match expected",
		},
		{
			name: "Authorizer Test",
			validatingPolicy: &admissionregv1.ValidatingAdmissionPolicy{
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{Expression: `authorizer.group("").resource("pods").namespace("default").check("create").allowed()`},
					},
				},
			},
			testCase: MockTestCase{
				Object:        validPod,
				ExpectAllowed: true,
				Authorizer: []AuthorizationMockConfig{
					{
						Group:     "",
						Resource:  "pods",
						Namespace: "default",
						Verb:      "create",
						Decision:  "allow",
					},
				},
				UserInfo: &user.DefaultInfo{Name: "admin"},
			},
			wantPassed: true,
		},
		{
			name: "Warning Expected But None Got",
			validatingPolicy: &admissionregv1.ValidatingAdmissionPolicy{
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{{Expression: "true"}},
				},
			},
			testCase: MockTestCase{
				Object:         validPod,
				ExpectAllowed:  true,
				ExpectWarnings: []string{"some warning"},
			},
			wantPassed:  false,
			wantMessage: "expected warnings [some warning], got none",
		},
		{
			name: "Warning Count Mismatch",
			validatingPolicy: &admissionregv1.ValidatingAdmissionPolicy{
				Spec: admissionregv1.ValidatingAdmissionPolicySpec{
					Validations: []admissionregv1.Validation{
						{Expression: "false", Message: "warn1"},
						{Expression: "false", Message: "warn2"},
					},
				},
			},
			validatingBinding: &admissionregv1.ValidatingAdmissionPolicyBinding{
				Spec: admissionregv1.ValidatingAdmissionPolicyBindingSpec{
					ValidationActions: []admissionregv1.ValidationAction{admissionregv1.Warn},
				},
			},
			testCase: MockTestCase{
				Object:         validPod,
				ExpectAllowed:  true,
				ExpectWarnings: []string{"warn1", "extra_warning"},
			},
			wantPassed:  false,
			wantMessage: "expected 2 warnings, got 1",
		},
		{
			name: "Mutating Policy Evaluation Error",
			mutatingPolicy: &admissionv1beta1.MutatingAdmissionPolicy{
				Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
					Mutations: []admissionv1beta1.Mutation{
						{
							PatchType: admissionv1beta1.PatchTypeJSONPatch,
							JSONPatch: &admissionv1beta1.JSONPatch{
								Expression: `invalid_syntax(`, // Invalid CEL
							},
						},
					},
				},
			},
			testCase: MockTestCase{
				Object:        validPod,
				ExpectAllowed: true,
			},
			wantPassed:  false,
			wantMessage: "evaluation error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := evaluator.EvaluateTest(tc.mutatingPolicy, tc.validatingPolicy, tc.validatingBinding, tc.testCase)

			if result.Passed != tc.wantPassed {
				t.Errorf("EvaluateTest() Passed = %v, want %v. Message: %s", result.Passed, tc.wantPassed, result.Message)
			}

			if tc.wantMessage != "" {
				if !tc.wantPassed && !strings.Contains(result.Message, tc.wantMessage) {
					t.Errorf("EvaluateTest() Message = %q, want to contain %q", result.Message, tc.wantMessage)
				}
			}
		})
	}
}
