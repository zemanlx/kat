package evaluator

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	admissionregv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// fixtures is the directory of the kat policies the compiled tests load.
const fixtures = "../test-policies-pass/validating"

func loadFixture(t *testing.T, path string, into any) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(fixtures, path))
	if err != nil {
		t.Fatal(err)
	}

	if err := yaml.Unmarshal(data, into); err != nil {
		t.Fatal(err)
	}
}

func loadObject(t *testing.T, path string) *unstructured.Unstructured {
	t.Helper()

	u := &unstructured.Unstructured{}
	loadFixture(t, path, &u.Object)

	return u
}

// loadPolicy loads the policy and the binding of a fixture directory.
func loadPolicy(t *testing.T, dir string) (*admissionregv1.ValidatingAdmissionPolicy, *admissionregv1.ValidatingAdmissionPolicyBinding) {
	t.Helper()

	policy := &admissionregv1.ValidatingAdmissionPolicy{}
	loadFixture(t, filepath.Join(dir, "policy.yaml"), policy)

	binding := &admissionregv1.ValidatingAdmissionPolicyBinding{}
	loadFixture(t, filepath.Join(dir, "binding.yaml"), binding)

	return policy, binding
}

type compiledCase struct {
	name    string
	policy  *admissionregv1.ValidatingAdmissionPolicy
	binding *admissionregv1.ValidatingAdmissionPolicyBinding
	req     Request
	allowed bool
	message string
	invalid bool
}

func (tt compiledCase) run(t *testing.T, e *Evaluator) {
	t.Helper()

	c, err := e.CompileValidating(tt.policy, tt.binding)
	if tt.invalid {
		if _, ok := errors.AsType[*InvalidPolicyError](err); !ok {
			t.Fatalf("CompileValidating error = %v, want *InvalidPolicyError", err)
		}

		return
	}

	if err != nil {
		t.Fatal(err)
	}

	got, err := c.Evaluate(t.Context(), tt.req)
	if err != nil {
		t.Fatal(err)
	}

	if got.Allowed != tt.allowed || got.Message != tt.message {
		t.Errorf("allowed=%v message=%q, want allowed=%v message=%q", got.Allowed, got.Message, tt.allowed, tt.message)
	}
}

func TestCompileValidatingEvaluate(t *testing.T) {
	t.Parallel()

	e, err := New()
	if err != nil {
		t.Fatal(err)
	}

	owner, ownerBinding := loadPolicy(t, "require-owner-label")
	labels, labelsBinding := loadPolicy(t, "require-labels-with-params")

	invalid := owner.DeepCopy()
	invalid.Spec.Validations[0].Expression = "object.("

	deployments := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	ownerNamespace := loadObject(t, "require-owner-label/tests/require-owner-label.with-label.allow.namespaceObject.yaml")
	labelConfig := []runtime.Object{loadObject(t, "require-labels-with-params/tests/require-labels-params.has-all-labels.allow.params.yaml")}
	ownerTests := "require-owner-label/tests/require-owner-label."
	labelTests := "require-labels-with-params/tests/require-labels-params."

	tests := []compiledCase{
		{
			name: "allowed object", policy: owner, binding: ownerBinding, allowed: true,
			req: Request{Object: loadObject(t, ownerTests+"with-label.allow.object.yaml"), Namespace: ownerNamespace, Resource: deployments},
		},
		{
			name: "denied object", policy: owner, binding: ownerBinding,
			req:     Request{Object: loadObject(t, ownerTests+"without-label.deny.object.yaml"), Namespace: ownerNamespace, Resource: deployments},
			message: "All workloads must have an 'owner' label",
		},
		{
			name: "params allowed", policy: labels, binding: labelsBinding, allowed: true,
			req: Request{Object: loadObject(t, labelTests+"has-all-labels.allow.object.yaml"), Resource: deployments, Params: labelConfig},
		},
		{
			name: "params denied", policy: labels, binding: labelsBinding,
			req:     Request{Object: loadObject(t, labelTests+"missing-label.deny.object.yaml"), Resource: deployments, Params: labelConfig},
			message: "Missing required labels: owner,team,environment",
		},
		{
			name: "missing params with Deny", policy: labels, binding: labelsBinding,
			req:     Request{Object: loadObject(t, labelTests+"has-all-labels.allow.object.yaml"), Resource: deployments},
			message: "failed to configure binding: " + errParamsNotFound.Error(),
		},
		{
			name: "invalid policy", policy: invalid, binding: ownerBinding, invalid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.run(t, e)
		})
	}
}
