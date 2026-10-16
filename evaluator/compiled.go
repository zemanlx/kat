package evaluator

import (
	"context"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
)

// CompiledValidating is a ValidatingAdmissionPolicy and one of its bindings,
// created as the API server would, ready to evaluate many objects.
type CompiledValidating struct {
	*validator

	binding *admissionregv1.ValidatingAdmissionPolicyBinding
}

// CompileValidating defaults and validates the policy and the binding as the
// API server does on creation, and compiles the policy's CEL. A nil binding
// is the default binding: Deny, no params.
func (e *Evaluator) CompileValidating(
	policy *admissionregv1.ValidatingAdmissionPolicy,
	binding *admissionregv1.ValidatingAdmissionPolicyBinding,
) (*CompiledValidating, error) {
	var bindings []*admissionregv1.ValidatingAdmissionPolicyBinding
	if binding != nil {
		bindings = append(bindings, binding)
	}

	policy, created, err := e.createValidating(policy, bindings)
	if err != nil {
		return nil, err
	}

	v, err := e.compileValidating(policy)
	if err != nil {
		return nil, err
	}

	return &CompiledValidating{binding: created[0], validator: v}, nil
}

// Request is an existing object evaluated as if it were being created.
type Request struct {
	Object *unstructured.Unstructured
	// Namespace is the object's Namespace, nil for cluster-scoped objects.
	Namespace *unstructured.Unstructured
	Resource  schema.GroupVersionResource
	// Params are resolved by the caller, so selector paramRefs work.
	// ponytail: Params are trusted; their name and namespace are not checked against paramRef.
	Params []runtime.Object
}

// Evaluate admits Object as a CREATE by a fixed audit user with a deny-all authorizer.
func (c *CompiledValidating) Evaluate(ctx context.Context, req Request) (*EvaluationResult, error) {
	r, err := newRequest(&admissionv1.AdmissionRequest{
		Operation: admissionv1.Create,
		Kind:      metav1.GroupVersionKind(req.Object.GroupVersionKind()),
		Resource:  metav1.GroupVersionResource(req.Resource),
		Namespace: req.Object.GetNamespace(),
		Name:      req.Object.GetName(),
	}, req.Object, nil, nil, req.Namespace, nil, &user.DefaultInfo{Name: "system:kat:audit"})
	if err != nil {
		return nil, err
	}

	out := &validatingOutcome{}

	params := func() ([]runtime.Object, error) {
		ref := c.binding.Spec.ParamRef
		if c.policy.Spec.ParamKind == nil || ref == nil {
			return []runtime.Object{nil}, nil
		}

		if len(req.Params) > 0 {
			return req.Params, nil
		}

		return noParams(ref)
	}

	if err := c.evaluateBinding(ctx, out, c.binding, r, params); err != nil {
		return nil, err
	}

	return out.result(), nil
}
