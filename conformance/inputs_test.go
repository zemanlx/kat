package conformance

import (
	"context"
	"encoding/json"
	"fmt"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	plugincel "k8s.io/apiserver/pkg/admission/plugin/cel"

	"github.com/zemanlx/kat/internal/loader"
)

// dryRun is the dry-run mode of every CREATE, UPDATE and DELETE request.
func dryRun() []string { return []string{metav1.DryRunAll} }

// prepareInputs computes, before any policy is installed, the inputs the
// server's CEL will see for the case, so that kat is given exactly those.
func (h *shardRun) prepareInputs(ctx context.Context, c *katCase) error {
	run := &c.run

	if c.namespaced {
		ns, err := h.srv.client.CoreV1().Namespaces().Get(ctx, c.namespace, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get namespace: %w", err)
		}

		run.namespaceObj, err = toUnstructured(plugincel.CreateNamespaceObject(ns))
		if err != nil {
			return err
		}
	}

	if err := h.prepareParams(ctx, c); err != nil {
		return err
	}

	if err := h.prepareObjects(ctx, c); err != nil {
		return err
	}

	run.request = katRequest(c)

	return nil
}

func (h *shardRun) prepareParams(ctx context.Context, c *katCase) error {
	if c.paramsKey == nil {
		return nil
	}

	stored, err := h.srv.get(ctx, *c.paramsKey)
	if err != nil {
		return err
	}

	c.run.params = withoutManagedFields(stored)

	return nil
}

// prepareObjects gets the server-defaulted object with a dry-run request and
// the stored oldObject. For UPDATE, the stored object's server-managed
// metadata is copied into the object, as a client doing read-modify-write
// would.
func (h *shardRun) prepareObjects(ctx context.Context, c *katCase) error {
	run := &c.run
	key := objectKey{resource: c.gvr, namespace: c.namespace, name: c.objName}

	switch c.op {
	case admissionv1.Create:
		run.send = withoutServerFields(c.object)

		created, err := h.srv.resource(key).Create(ctx, run.send.DeepCopy(), metav1.CreateOptions{DryRun: dryRun()})
		if err != nil {
			return fmt.Errorf("the server rejects the fixture object even without policies: %w", err)
		}

		run.object = withoutServerFields(created)
	case admissionv1.Update:
		stored, err := h.srv.get(ctx, key)
		if err != nil {
			return err
		}

		run.oldObject = withoutManagedFields(stored)
		run.send = withStoredMetadata(withoutServerFields(c.object), stored)

		updated, err := h.srv.resource(key).Update(ctx, run.send.DeepCopy(), metav1.UpdateOptions{DryRun: dryRun()})
		if err != nil {
			return fmt.Errorf("the server rejects the fixture update even without policies: %w", err)
		}

		run.object = withoutManagedFields(updated)
	case admissionv1.Delete:
		stored, err := h.srv.get(ctx, key)
		if err != nil {
			return err
		}

		run.oldObject = withoutManagedFields(stored)
	case admissionv1.Connect:
		opts := c.exec.DeepCopy()
		opts.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "PodExecOptions"}

		var err error
		if run.object, err = toUnstructured(opts); err != nil {
			return err
		}
	}

	return nil
}

// withStoredMetadata copies the metadata a client reads back from the server
// into an object it is about to update.
func withStoredMetadata(u, stored *unstructured.Unstructured) *unstructured.Unstructured {
	u.SetUID(stored.GetUID())
	u.SetResourceVersion(stored.GetResourceVersion())
	u.SetCreationTimestamp(stored.GetCreationTimestamp())
	u.SetGeneration(stored.GetGeneration())

	return u
}

// katRequest is the admission request the server builds for the case.
func katRequest(c *katCase) *admissionv1.AdmissionRequest {
	extra := make(map[string]authenticationv1.ExtraValue, len(c.user.Extra))
	for k, v := range c.user.Extra {
		extra[k] = v
	}

	dryRun := c.op != admissionv1.Connect
	req := &admissionv1.AdmissionRequest{
		UID:         types.UID("kat-conformance"),
		Kind:        metav1.GroupVersionKind(c.gvk),
		Resource:    metav1.GroupVersionResource(c.gvr),
		SubResource: c.subresource,
		Name:        c.objName,
		Namespace:   c.namespace,
		Operation:   c.op,
		UserInfo:    authenticationv1.UserInfo{Username: c.user.Name, UID: c.user.UID, Groups: c.user.Groups, Extra: extra},
		DryRun:      &dryRun,
	}

	if opts := requestOptions(c.op); opts != nil {
		raw, _ := json.Marshal(opts) //nolint:errchkjson // A map of strings always marshals.
		req.Options = runtime.RawExtension{Raw: raw}
	}

	return req
}

// requestOptions is request.options as the server passes it to CEL: the
// operation's options with the harness's dry-run, and null for CONNECT.
func requestOptions(op admissionv1.Operation) map[string]any {
	kind := map[admissionv1.Operation]string{
		admissionv1.Create: "CreateOptions",
		admissionv1.Update: "UpdateOptions",
		admissionv1.Delete: "DeleteOptions",
	}[op]
	if kind == "" {
		return nil
	}

	return map[string]any{"apiVersion": "meta.k8s.io/v1", "kind": kind, "dryRun": []any{metav1.DryRunAll}}
}

// evaluateKat runs kat's evaluator on the prepared inputs and, when kat
// mutated the object, passes kat's output through the same server defaulting
// the server applies after its own mutation.
func (h *shardRun) evaluateKat(ctx context.Context, c *katCase) {
	run := &c.run
	tc := &loader.TestCase{
		Name:         c.tc.Name,
		PolicyName:   c.tc.PolicyName,
		Request:      run.request,
		Object:       run.object,
		OldObject:    run.oldObject,
		Params:       run.params,
		NamespaceObj: run.namespaceObj,
		UserInfo:     c.user,
		Authorizer:   c.tc.Authorizer,
	}

	run.katResult, run.katErr = h.eval.Evaluate(c.mutatingPolicy, c.mutatingBinding, c.validatingPolicy, c.validatingBinding, tc)
	if run.katErr != nil || run.katResult == nil || !run.katResult.Allowed {
		return
	}

	if c.op != admissionv1.Create && c.op != admissionv1.Update {
		return
	}

	if run.katResult.PatchedObject == nil {
		run.katObject = normalizeObject(run.object)

		return
	}

	run.katObject, run.katObjectErr = h.defaultKatOutput(ctx, c, run.katResult.PatchedObject)
}

func (h *shardRun) defaultKatOutput(ctx context.Context, c *katCase, patched *unstructured.Unstructured) (map[string]any, string) {
	key := objectKey{resource: c.gvr, namespace: c.namespace, name: c.objName}

	var (
		out *unstructured.Unstructured
		err error
	)

	if c.op == admissionv1.Create {
		out, err = h.srv.resource(key).Create(ctx, withoutServerFields(patched), metav1.CreateOptions{DryRun: dryRun()})
	} else {
		out, err = h.srv.resource(key).Update(ctx, patched.DeepCopy(), metav1.UpdateOptions{DryRun: dryRun()})
	}

	if err != nil {
		return nil, "the server rejects kat's mutated object: " + err.Error()
	}

	return normalizeObject(out), ""
}

// evaluateRawFixture runs kat on the fixture as written, the way the kat
// binary does, for the informational realism check.
func (h *shardRun) evaluateRawFixture(c *katCase) outcome {
	res, err := h.eval.Evaluate(c.mutatingPolicy, c.mutatingBinding, c.validatingPolicy, c.validatingBinding, c.tc)

	return katOutcome(res, err, nil, "")
}

func toUnstructured(obj any) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("convert %T: %w", obj, err)
	}

	return &unstructured.Unstructured{Object: m}, nil
}

// namespaceWithType adds the apiVersion and kind that kat's .request.yaml
// requires on namespaceObject; the server's namespaceObject has neither.
func namespaceWithType(u *unstructured.Unstructured) *unstructured.Unstructured {
	out := u.DeepCopy()
	out.SetAPIVersion("v1")
	out.SetKind("Namespace")

	return out
}
