package conformance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	admissionregv1 "k8s.io/api/admissionregistration/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// probeName names the harness's readiness probe policies, bindings and Lease.
const probeName = "kat-probe"

// probeReady is the probe VAP's denial once the probe MAP has mutated the
// probe Lease.
const probeReady = "ValidatingAdmissionPolicy '" + probeName + "' with binding '" + probeName +
	"' denied request: kat-probe mutated=true"

var errProbeNotReady = errors.New("admission policies did not become active")

// installPolicies creates the suite's policies and bindings. A policy or
// binding the server rejects is recorded on the cases that use it.
func (h *shardRun) installPolicies(ctx context.Context) {
	api := h.srv.client.AdmissionregistrationV1()
	create := metav1.CreateOptions{}

	for _, p := range h.suite.MutatingPolicies {
		_, err := api.MutatingAdmissionPolicies().Create(ctx, clean(p), create)
		h.rejected(p.Name, err)
	}

	for _, b := range h.suite.MutatingBindings {
		_, err := api.MutatingAdmissionPolicyBindings().Create(ctx, clean(b), create)
		h.rejected(b.Spec.PolicyName, err)
	}

	for _, p := range h.suite.ValidatingPolicies {
		_, err := api.ValidatingAdmissionPolicies().Create(ctx, clean(p), create)
		h.rejected(p.Name, err)
	}

	for _, b := range h.suite.ValidatingBindings {
		_, err := api.ValidatingAdmissionPolicyBindings().Create(ctx, clean(b), create)
		h.rejected(b.Spec.PolicyName, err)
	}
}

// rejected records the first error of a policy's creation on its cases.
func (h *shardRun) rejected(policyName string, err error) {
	if err == nil {
		return
	}

	for _, c := range h.shard.cases {
		if c.policyName == policyName && c.run.server.policyErr == "" {
			c.run.server.policyErr = err.Error()
		}
	}
}

// clean drops server-owned metadata so that a loaded object can be created.
func clean[T interface{ SetResourceVersion(v string) }](obj T) T {
	obj.SetResourceVersion("")

	return obj
}

// installProbe creates the probe policies after the suite's. Each admission
// plugin compiles policies from its informers, which deliver objects in
// resourceVersion order, so once the probe is active every earlier policy is
// active too. The MAP labels the probe Lease and the VAP reports whether it
// saw the label, so a single request shows that both plugins caught up.
func (h *shardRun) installProbe(ctx context.Context) error {
	api := h.srv.client.AdmissionregistrationV1()
	never := admissionregv1.NeverReinvocationPolicy
	fail := admissionregv1.Fail
	match := &admissionregv1.MatchResources{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": probeNamespace}},
		ResourceRules: []admissionregv1.NamedRuleWithOperations{{
			Operations:  []admissionregv1.OperationType{admissionregv1.Create},
			APIGroups:   []string{coordinationv1.GroupName},
			APIVersions: []string{"v1"},
			Resources:   []string{"leases"},
		}},
	}
	conditions := []admissionregv1.MatchCondition{{Name: probeName, Expression: "object.metadata.name == '" + probeName + "'"}}
	meta := metav1.ObjectMeta{Name: probeName}

	mutating := &admissionregv1.MutatingAdmissionPolicy{ObjectMeta: meta, Spec: admissionregv1.MutatingAdmissionPolicySpec{
		MatchConstraints: match, MatchConditions: conditions, FailurePolicy: &fail, ReinvocationPolicy: never,
		Mutations: []admissionregv1.Mutation{{
			PatchType: admissionregv1.PatchTypeApplyConfiguration,
			ApplyConfiguration: &admissionregv1.ApplyConfiguration{
				Expression: `Object{metadata: Object.metadata{labels: {"` + probeName + `": "mutated"}}}`,
			},
		}},
	}}
	validating := &admissionregv1.ValidatingAdmissionPolicy{ObjectMeta: meta, Spec: admissionregv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: match, MatchConditions: conditions, FailurePolicy: &fail,
		Validations: []admissionregv1.Validation{{
			Expression:        "false",
			MessageExpression: `"kat-probe mutated=" + string(object.metadata.?labels["` + probeName + `"].orValue("") == "mutated")`,
		}},
	}}

	if _, err := api.MutatingAdmissionPolicies().Create(ctx, mutating, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create probe MutatingAdmissionPolicy: %w", err)
	}

	if _, err := api.MutatingAdmissionPolicyBindings().Create(ctx, &admissionregv1.MutatingAdmissionPolicyBinding{
		ObjectMeta: meta, Spec: admissionregv1.MutatingAdmissionPolicyBindingSpec{PolicyName: probeName},
	}, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create probe MutatingAdmissionPolicyBinding: %w", err)
	}

	if _, err := api.ValidatingAdmissionPolicies().Create(ctx, validating, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create probe ValidatingAdmissionPolicy: %w", err)
	}

	if _, err := api.ValidatingAdmissionPolicyBindings().Create(ctx, &admissionregv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: meta, Spec: admissionregv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName: probeName, ValidationActions: []admissionregv1.ValidationAction{admissionregv1.Deny},
		},
	}, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create probe ValidatingAdmissionPolicyBinding: %w", err)
	}

	return nil
}

// waitProbe polls a dry-run create of the probe Lease until both probe
// policies act on it.
func (h *shardRun) waitProbe(ctx context.Context) error {
	const (
		interval = 100 * time.Millisecond
		timeout  = 60 * time.Second
	)

	lease := &coordinationv1.Lease{Name: probeName, Namespace: probeNamespace}
	last := ""

	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := h.srv.client.CoordinationV1().Leases(probeNamespace).Create(ctx, lease, metav1.CreateOptions{DryRun: dryRun()})
		if err == nil {
			last = "probe Lease admitted"

			return false, nil
		}

		last = err.Error()

		return strings.Contains(last, probeReady), nil
	})
	if err != nil {
		return fmt.Errorf("%w: last probe response: %s: %w", errProbeNotReady, last, err)
	}

	return nil
}
