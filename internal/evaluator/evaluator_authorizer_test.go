package evaluator

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	admissionv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// TestEvaluateMutating_WithAuthorizer tests mutating policies that use authorizer.
//
//nolint:funlen // tests table can be long
func TestEvaluateMutating_WithAuthorizer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		policy          *admissionv1beta1.MutatingAdmissionPolicy
		object          *unstructured.Unstructured
		authorizer      *MockAuthorizer
		username        string
		groups          []string
		expectedMutated bool
		expectedObject  *unstructured.Unstructured
	}{
		{
			name: "add label only if user has permission",
			policy: makeMutatingPolicy(`authorizer.group("").resource("pods").namespace(object.metadata.namespace).check("create").allowed() ?
				[JSONPatch{op: "add", path: "/metadata/labels/approved", value: "true"}] : []`),
			object: makePodObject("test-pod", "default"),
			authorizer: func() *MockAuthorizer {
				m := NewMockAuthorizer()
				m.Allow("", "pods", "", "default", "create")

				return m
			}(),
			username:        "admin",
			groups:          []string{"system:authenticated"},
			expectedMutated: true,
			expectedObject:  makePodWithLabels("test-pod", "default", map[string]any{"approved": "true"}),
		},
		{
			name: "no mutation when user lacks permission",
			policy: makeMutatingPolicy(`authorizer.group("").resource("pods").namespace(object.metadata.namespace).check("create").allowed() ?
				[JSONPatch{op: "add", path: "/metadata/labels/approved", value: "true"}] : []`),
			object: makePodObject("test-pod", "default"),
			authorizer: func() *MockAuthorizer {
				m := NewMockAuthorizer()
				m.Deny("", "pods", "", "default", "create")

				return m
			}(),
			username:        "user",
			groups:          []string{"system:authenticated"},
			expectedMutated: false,
		},
		{
			name: "add annotation with permission check verb",
			policy: makeMutatingPolicy(`[JSONPatch{
				op: "add",
				path: "/metadata/annotations/can-delete",
				value: authorizer.group("").resource("pods").namespace(object.metadata.namespace).check("delete").allowed() ? "yes" : "no"
			}]`),
			object: makePodWithAnnotations("test-pod", "default", map[string]any{}),
			authorizer: func() *MockAuthorizer {
				m := NewMockAuthorizer()
				m.Allow("", "pods", "", "default", "delete")

				return m
			}(),
			username:        "admin",
			groups:          []string{"system:authenticated"},
			expectedMutated: true,
			expectedObject:  makePodWithAnnotations("test-pod", "default", map[string]any{"can-delete": "yes"}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runMutatingTest(t, tc.policy, tc.object, tc.authorizer, tc.username, tc.groups, tc.expectedMutated, tc.expectedObject)
		})
	}
}

func runMutatingTest(t *testing.T, policy *admissionv1beta1.MutatingAdmissionPolicy, object *unstructured.Unstructured, auth *MockAuthorizer, username string, groups []string, expectedMutated bool, expectedObject *unstructured.Unstructured) {
	t.Helper()

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

	userInfo := MockUserInfo(username, groups)

	result, err := evaluator.EvaluateMutating(policy, nil, request, object, nil, nil, nil, auth, userInfo)
	if err != nil {
		t.Fatalf("EvaluateMutating() error = %v", err)
	}

	if !result.Allowed {
		t.Errorf("EvaluateMutating() Allowed = false, want true")
	}

	if result.PatchedObject == nil {
		t.Fatal("EvaluateMutating() should return patched object")
	}

	wantObject := expectedObject
	if !expectedMutated {
		wantObject = object
	}

	if diff := cmp.Diff(wantObject.Object, result.PatchedObject.Object); diff != "" {
		t.Errorf("Patched object mismatch (-want +got):\n%s", diff)
	}
}

func makeMutatingPolicy(expression string) *admissionv1beta1.MutatingAdmissionPolicy {
	return &admissionv1beta1.MutatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
		Spec: admissionv1beta1.MutatingAdmissionPolicySpec{
			Mutations: []admissionv1beta1.Mutation{
				{
					PatchType: admissionv1beta1.PatchTypeJSONPatch,
					JSONPatch: &admissionv1beta1.JSONPatch{
						Expression: expression,
					},
				},
			},
		},
	}
}

//nolint:unparam // namespace is always default in current tests
func makePodObject(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
				"labels":    map[string]any{},
			},
		},
	}
}

func makePodWithLabels(name, namespace string, labels map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
				"labels":    labels,
			},
		},
	}
}

func makePodWithAnnotations(name, namespace string, annotations map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":        name,
				"namespace":   namespace,
				"annotations": annotations,
			},
		},
	}
}

// TestEvaluateValidating_WithAuthorizer tests validating policies that use authorizer.
//
//nolint:funlen // tests table can be long
func TestEvaluateValidating_WithAuthorizer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		policy        *admissionregv1.ValidatingAdmissionPolicy
		object        *unstructured.Unstructured
		authorizer    *MockAuthorizer
		username      string
		groups        []string
		expectAllowed bool
		expectMessage string
	}{
		{
			name: "allow when user has permission",
			policy: makeValidatingPolicy("",
				`authorizer.group("").resource("pods").namespace(object.metadata.namespace).check("create").allowed()`,
				"User does not have permission to create pods"),
			object: makePodObject("test-pod", "default"),
			authorizer: func() *MockAuthorizer {
				m := NewMockAuthorizer()
				m.Allow("", "pods", "", "default", "create")

				return m
			}(),
			username:      "admin",
			groups:        []string{"system:authenticated"},
			expectAllowed: true,
		},
		{
			name: "deny when user lacks permission",
			policy: makeValidatingPolicy("",
				`authorizer.group("").resource("pods").namespace(object.metadata.namespace).check("create").allowed()`,
				"User does not have permission to create pods"),
			object: makePodObject("test-pod", "default"),
			authorizer: func() *MockAuthorizer {
				m := NewMockAuthorizer()
				m.Deny("", "pods", "", "default", "create")

				return m
			}(),
			username:      "user",
			groups:        []string{"system:authenticated"},
			expectAllowed: false,
			expectMessage: "User does not have permission to create pods",
		},
		{
			name: "check delete permission for privileged operations",
			policy: makeValidatingPolicy("",
				`!has(object.spec.hostNetwork) || !object.spec.hostNetwork ||
				authorizer.group("").resource("pods").namespace(object.metadata.namespace).check("deletecollection").allowed()`,
				"Only users with deletecollection permission can use hostNetwork"),
			object: makePodWithSpec("test-pod", "default", map[string]any{"hostNetwork": true}),
			authorizer: func() *MockAuthorizer {
				m := NewMockAuthorizer()
				m.Allow("", "pods", "", "default", "deletecollection")

				return m
			}(),
			username:      "admin",
			groups:        []string{"system:authenticated"},
			expectAllowed: true,
		},
		{
			name: "deny privileged operations without permission",
			policy: makeValidatingPolicy("",
				`!has(object.spec.hostNetwork) || !object.spec.hostNetwork ||
				authorizer.group("").resource("pods").namespace(object.metadata.namespace).check("deletecollection").allowed()`,
				"Only users with deletecollection permission can use hostNetwork"),
			object: makePodWithSpec("test-pod", "default", map[string]any{"hostNetwork": true}),
			authorizer: func() *MockAuthorizer {
				m := NewMockAuthorizer()
				m.Deny("", "pods", "", "default", "deletecollection")

				return m
			}(),
			username:      "user",
			groups:        []string{"system:authenticated"},
			expectAllowed: false,
			expectMessage: "Only users with deletecollection permission can use hostNetwork",
		},
		{
			name: "allow when match condition uses authorizer - policy evaluated",
			policy: makeValidatingPolicy(
				`authorizer.group("").resource("pods").namespace(object.metadata.namespace).check("update").allowed()`,
				`object.metadata.name.startsWith("privileged-")`,
				"Pods for privileged users must have 'privileged-' prefix"),
			object: makePodObject("privileged-pod", "default"),
			authorizer: func() *MockAuthorizer {
				m := NewMockAuthorizer()
				m.Allow("", "pods", "", "default", "update")

				return m
			}(),
			username:      "admin",
			groups:        []string{"system:authenticated"},
			expectAllowed: true,
		},
		{
			name: "skip validation when match condition fails - policy not evaluated",
			policy: makeValidatingPolicy(
				`authorizer.group("").resource("pods").namespace(object.metadata.namespace).check("update").allowed()`,
				`object.metadata.name.startsWith("privileged-")`,
				"Pods for privileged users must have 'privileged-' prefix"),
			object: makePodObject("regular-pod", "default"),
			authorizer: func() *MockAuthorizer {
				m := NewMockAuthorizer()
				m.Deny("", "pods", "", "default", "update")

				return m
			}(),
			username:      "user",
			groups:        []string{"system:authenticated"},
			expectAllowed: true,
		},
		{
			name: "check multiple permissions with complex logic",
			policy: makeValidatingPolicy("",
				`object.spec.replicas <= 3 ||
				(authorizer.group("apps").resource("deployments").namespace(object.metadata.namespace).check("update").allowed() &&
				 authorizer.group("apps").resource("deployments").namespace(object.metadata.namespace).check("delete").allowed())`,
				"Deployments with >3 replicas require update and delete permissions"),
			object: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":      "test-deployment",
						"namespace": "default",
					},
					"spec": map[string]any{
						"replicas": int64(10),
					},
				},
			},
			authorizer: func() *MockAuthorizer {
				m := NewMockAuthorizer()
				m.Allow("apps", "deployments", "", "default", "update")
				m.Allow("apps", "deployments", "", "default", "delete")

				return m
			}(),
			username:      "admin",
			groups:        []string{"system:authenticated"},
			expectAllowed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runValidatingTest(t, tc.policy, tc.object, tc.authorizer, tc.username, tc.groups, tc.expectAllowed, tc.expectMessage)
		})
	}
}

func runValidatingTest(t *testing.T, policy *admissionregv1.ValidatingAdmissionPolicy, object *unstructured.Unstructured, auth *MockAuthorizer, username string, groups []string, expectAllowed bool, expectMessage string) {
	t.Helper()

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

	userInfo := MockUserInfo(username, groups)

	result, err := evaluator.EvaluateValidating(policy, nil, request, object, nil, nil, nil, auth, userInfo)
	if err != nil {
		t.Fatalf("EvaluateValidating() error = %v", err)
	}

	if result.Allowed != expectAllowed {
		t.Errorf("EvaluateValidating() Allowed = %v, want %v", result.Allowed, expectAllowed)
	}

	if expectMessage != "" && result.Message != expectMessage {
		t.Errorf("EvaluateValidating() Message = %q, want %q", result.Message, expectMessage)
	}
}

func makeValidatingPolicy(matchExpression, validateExpression, message string) *admissionregv1.ValidatingAdmissionPolicy {
	spec := admissionregv1.ValidatingAdmissionPolicySpec{}
	if matchExpression != "" {
		spec.MatchConditions = []admissionregv1.MatchCondition{
			{
				Name:       "match-condition",
				Expression: matchExpression,
			},
		}
	}

	if validateExpression != "" {
		spec.Validations = []admissionregv1.Validation{
			{
				Expression: validateExpression,
				Message:    message,
			},
		}
	}

	return &admissionregv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
		Spec:       spec,
	}
}

func makePodWithSpec(name, namespace string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}
}
