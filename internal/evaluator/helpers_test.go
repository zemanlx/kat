package evaluator

import (
	"context"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

// validVAP completes a test's minimal policy with the fields the API server
// requires: a name and matchConstraints that select every request.
func validVAP(p *admissionregv1.ValidatingAdmissionPolicy) *admissionregv1.ValidatingAdmissionPolicy {
	p = p.DeepCopy()
	if p.Name == "" {
		p.Name = "test-policy"
	}

	if p.Spec.MatchConstraints == nil {
		p.Spec.MatchConstraints = matchAll(admissionregv1.OperationAll)
	}

	return p
}

// validMAP is validVAP for a MutatingAdmissionPolicy, which also requires a
// reinvocationPolicy.
func validMAP(p *admissionregv1.MutatingAdmissionPolicy) *admissionregv1.MutatingAdmissionPolicy {
	p = p.DeepCopy()
	if p.Name == "" {
		p.Name = "test-policy"
	}

	if p.Spec.MatchConstraints == nil {
		p.Spec.MatchConstraints = matchAll(admissionregv1.OperationAll)
	}

	if p.Spec.ReinvocationPolicy == "" {
		p.Spec.ReinvocationPolicy = admissionregv1.NeverReinvocationPolicy
	}

	return p
}

func matchAll(operations ...admissionregv1.OperationType) *admissionregv1.MatchResources {
	return &admissionregv1.MatchResources{ResourceRules: []admissionregv1.NamedRuleWithOperations{{
		Operations:  operations,
		APIGroups:   []string{"*"},
		APIVersions: []string{"*"},
		Resources:   []string{"*"},
	}}}
}

func configMapParams() *admissionregv1.ParamKind {
	return &admissionregv1.ParamKind{APIVersion: "v1", Kind: "ConfigMap"}
}

// withParamsVAP declares params on the policy and returns a binding that
// passes them.
func withParamsVAP(p *admissionregv1.ValidatingAdmissionPolicy) (*admissionregv1.ValidatingAdmissionPolicy, *admissionregv1.ValidatingAdmissionPolicyBinding) {
	p = validVAP(p)
	p.Spec.ParamKind = configMapParams()

	return p, &admissionregv1.ValidatingAdmissionPolicyBinding{
		Name: "test-binding",
		Spec: admissionregv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName:        p.Name,
			ParamRef:          &admissionregv1.ParamRef{Name: "params", ParameterNotFoundAction: new(admissionregv1.DenyAction)},
			ValidationActions: []admissionregv1.ValidationAction{admissionregv1.Deny},
		},
	}
}

func withParamsMAP(p *admissionregv1.MutatingAdmissionPolicy) (*admissionregv1.MutatingAdmissionPolicy, *admissionregv1.MutatingAdmissionPolicyBinding) {
	p = validMAP(p)
	p.Spec.ParamKind = configMapParams()

	return p, &admissionregv1.MutatingAdmissionPolicyBinding{
		Name: "test-binding",
		Spec: admissionregv1.MutatingAdmissionPolicyBindingSpec{
			PolicyName: p.Name,
			ParamRef:   &admissionregv1.ParamRef{Name: "params", ParameterNotFoundAction: new(admissionregv1.DenyAction)},
		},
	}
}

func validVAPB(b *admissionregv1.ValidatingAdmissionPolicyBinding, policy string) *admissionregv1.ValidatingAdmissionPolicyBinding {
	b = b.DeepCopy()
	if b.Name == "" {
		b.Name = "test-binding"
	}

	if b.Spec.PolicyName == "" {
		b.Spec.PolicyName = policy
	}

	return b
}

func validMAPB(b *admissionregv1.MutatingAdmissionPolicyBinding, policy string) *admissionregv1.MutatingAdmissionPolicyBinding {
	b = b.DeepCopy()
	if b.Name == "" {
		b.Name = "test-binding"
	}

	if b.Spec.PolicyName == "" {
		b.Spec.PolicyName = policy
	}

	return b
}

// EvaluateMutating admits a request with a MutatingAdmissionPolicy and one
// binding, which may be nil.
func (e *Evaluator) EvaluateMutating(
	policy *admissionregv1.MutatingAdmissionPolicy,
	binding *admissionregv1.MutatingAdmissionPolicyBinding,
	request *admissionv1.AdmissionRequest,
	object, oldObject, params, namespaceObj *unstructured.Unstructured,
	authz authorizer.Authorizer,
	userInfo user.Info,
) (*EvaluationResult, error) {
	req, err := newRequest(request, object, oldObject, params, namespaceObj, authz, userInfo)
	if err != nil {
		return nil, err
	}

	var bindings []*admissionregv1.MutatingAdmissionPolicyBinding
	if binding != nil {
		bindings = append(bindings, binding)
	}

	return e.admitMutating(context.Background(), policy, bindings, req)
}

// EvaluateValidating admits a request with a ValidatingAdmissionPolicy and
// one binding, which may be nil.
func (e *Evaluator) EvaluateValidating(
	policy *admissionregv1.ValidatingAdmissionPolicy,
	binding *admissionregv1.ValidatingAdmissionPolicyBinding,
	request *admissionv1.AdmissionRequest,
	object, oldObject, params, namespaceObj *unstructured.Unstructured,
	authz authorizer.Authorizer,
	userInfo user.Info,
) (*EvaluationResult, error) {
	req, err := newRequest(request, object, oldObject, params, namespaceObj, authz, userInfo)
	if err != nil {
		return nil, err
	}

	var bindings []*admissionregv1.ValidatingAdmissionPolicyBinding
	if binding != nil {
		bindings = append(bindings, binding)
	}

	return e.admitValidating(context.Background(), policy, bindings, req)
}
