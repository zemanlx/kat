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

// paramsProbeName names the probe for the shard policy's paramKind informer.
const paramsProbeName = probeName + "-params"

// paramsProbeLabels select the params probe's Lease.
var paramsProbeLabels = map[string]string{paramsProbeName: "true"} //nolint:gochecknoglobals // Constant.

// probeReady is the probe VAP's denial once the probe MAP has mutated the
// probe Lease.
const probeReady = "ValidatingAdmissionPolicy '" + probeName + "' with binding '" + probeName +
	"' denied request: kat-probe mutated=true"

var errProbeNotReady = errors.New("admission policies did not become active")

// installPolicies creates the shard's policy and its bindings. A policy or
// binding the server rejects is recorded on the cases that use it.
func (h *shardRun) installPolicies(ctx context.Context) {
	api := h.srv.client.AdmissionregistrationV1()
	create := metav1.CreateOptions{}
	name := h.shard.policy

	for _, p := range h.suite.MutatingPolicies {
		if p.Name == name {
			_, err := api.MutatingAdmissionPolicies().Create(ctx, clean(p), create)
			h.rejected(err)
		}
	}

	for _, b := range h.suite.MutatingBindings {
		if b.Spec.PolicyName == name {
			_, err := api.MutatingAdmissionPolicyBindings().Create(ctx, clean(b), create)
			h.rejected(err)
		}
	}

	for _, p := range h.suite.ValidatingPolicies {
		if p.Name == name {
			_, err := api.ValidatingAdmissionPolicies().Create(ctx, clean(p), create)
			h.rejected(err)
		}
	}

	for _, b := range h.suite.ValidatingBindings {
		if b.Spec.PolicyName == name {
			_, err := api.ValidatingAdmissionPolicyBindings().Create(ctx, clean(b), create)
			h.rejected(err)
		}
	}
}

// rejected records the first error of the policy's creation on its cases.
func (h *shardRun) rejected(err error) {
	if err == nil {
		return
	}

	for _, c := range h.shard.cases {
		if c.run.server.policyErr == "" {
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

	// Before the main probe, so that once it is active this one is too.
	if err := h.installParamsProbe(ctx, match); err != nil {
		return err
	}

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

// paramsKind is the shard policy's paramKind, and whether the policy is
// mutating; nil when it has none.
func (h *shardRun) paramsKind() (*admissionregv1.ParamKind, bool) {
	c := h.shard.cases[0]

	return c.paramKind(), c.policies.Mutating != nil
}

// installParamsProbe creates, when the shard's policy has a paramKind, a probe
// policy of the same plugin and paramKind whose binding references missing
// params under Deny. Its request fails with notSyncedMessage until the
// plugin's informer for that kind has synced, and then with another error
// (params not found, or the kind is not served). Without it, a policy under
// failurePolicy: Ignore would be skipped while the informer syncs.
func (h *shardRun) installParamsProbe(ctx context.Context, match *admissionregv1.MatchResources) error {
	kind, mutating := h.paramsKind()
	if kind == nil {
		return nil
	}

	api := h.srv.client.AdmissionregistrationV1()
	never := admissionregv1.NeverReinvocationPolicy
	fail := admissionregv1.Fail
	deny := admissionregv1.DenyAction
	meta := metav1.ObjectMeta{Name: paramsProbeName}
	ref := &admissionregv1.ParamRef{Name: probeName + "-missing", ParameterNotFoundAction: &deny}

	// The server collects params before it evaluates match conditions, so
	// only an object selector keeps this probe off the main probe's Lease.
	match = match.DeepCopy()
	match.ObjectSelector = &metav1.LabelSelector{MatchLabels: paramsProbeLabels}

	var conditions []admissionregv1.MatchCondition

	if mutating {
		if _, err := api.MutatingAdmissionPolicies().Create(ctx, &admissionregv1.MutatingAdmissionPolicy{ObjectMeta: meta, Spec: admissionregv1.MutatingAdmissionPolicySpec{
			ParamKind: kind, MatchConstraints: match, MatchConditions: conditions, FailurePolicy: &fail, ReinvocationPolicy: never,
			Mutations: []admissionregv1.Mutation{{
				PatchType:          admissionregv1.PatchTypeApplyConfiguration,
				ApplyConfiguration: &admissionregv1.ApplyConfiguration{Expression: `Object{}`},
			}},
		}}, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create params probe MutatingAdmissionPolicy: %w", err)
		}

		if _, err := api.MutatingAdmissionPolicyBindings().Create(ctx, &admissionregv1.MutatingAdmissionPolicyBinding{
			ObjectMeta: meta, Spec: admissionregv1.MutatingAdmissionPolicyBindingSpec{PolicyName: paramsProbeName, ParamRef: ref},
		}, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create params probe MutatingAdmissionPolicyBinding: %w", err)
		}

		return nil
	}

	if _, err := api.ValidatingAdmissionPolicies().Create(ctx, &admissionregv1.ValidatingAdmissionPolicy{ObjectMeta: meta, Spec: admissionregv1.ValidatingAdmissionPolicySpec{
		ParamKind: kind, MatchConstraints: match, MatchConditions: conditions, FailurePolicy: &fail,
		Validations: []admissionregv1.Validation{{Expression: "true"}},
	}}, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create params probe ValidatingAdmissionPolicy: %w", err)
	}

	if _, err := api.ValidatingAdmissionPolicyBindings().Create(ctx, &admissionregv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: meta, Spec: admissionregv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName: paramsProbeName, ParamRef: ref, ValidationActions: []admissionregv1.ValidationAction{admissionregv1.Deny},
		},
	}, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create params probe ValidatingAdmissionPolicyBinding: %w", err)
	}

	return nil
}

// waitProbe polls a dry-run create of the probe Lease until both probe
// policies act on it, and then the params probe until its informer synced.
func (h *shardRun) waitProbe(ctx context.Context) error {
	probe := &coordinationv1.Lease{Name: probeName, Namespace: probeNamespace}
	if err := h.waitLease(ctx, probe, func(msg string) bool { return strings.Contains(msg, probeReady) }); err != nil {
		return err
	}

	if kind, _ := h.paramsKind(); kind == nil {
		return nil
	}

	params := &coordinationv1.Lease{Name: paramsProbeName, Namespace: probeNamespace, Labels: paramsProbeLabels}

	return h.waitLease(ctx, params, func(msg string) bool { return !strings.Contains(msg, notSyncedMessage) })
}

// waitLease polls a dry-run create of the probe Lease until it fails with an
// error message for which done is true.
func (h *shardRun) waitLease(ctx context.Context, lease *coordinationv1.Lease, done func(msg string) bool) error {
	const (
		interval = 100 * time.Millisecond
		timeout  = 60 * time.Second
	)

	last := ""

	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := h.srv.client.CoordinationV1().Leases(probeNamespace).Create(ctx, lease, metav1.CreateOptions{DryRun: dryRun()})
		if err == nil {
			last = "probe Lease admitted"

			return false, nil
		}

		last = err.Error()

		return done(last), nil
	})
	if err != nil {
		return fmt.Errorf("%w: probe %s: last response: %s: %w", errProbeNotReady, lease.Name, last, err)
	}

	return nil
}
