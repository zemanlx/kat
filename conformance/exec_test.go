package conformance

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

var (
	// denialPattern matches a policy denial. Validating denials read
	// `<res> "<name>" is forbidden: ValidatingAdmissionPolicy '<p>' with
	// binding '<b>' denied request: <msg>`; mutating ones `policy '<p>' with
	// binding '<b>' denied request: <msg>`.
	denialPattern = regexp.MustCompile(`(?s)(?:ValidatingAdmissionPolicy|MutatingAdmissionPolicy|policy) '([^']*)' with binding '([^']*)' denied request: (.*)$`)
	// warningPattern matches a Warn validation action's warning.
	warningPattern = regexp.MustCompile(`(?s)^Validation failed for ValidatingAdmissionPolicy '([^']*)' with binding '([^']*)': (.*)$`)

	errIdentity = errors.New("the server saw a different identity than kat was given")
)

// notSyncedMessage marks a transient denial while a new paramKind informer
// syncs; the request is retried.
const notSyncedMessage = "not yet synced to use for admission"

// serverResult is the server's decision for one case.
type serverResult struct {
	// policyErr is set when the server rejected the policy or binding.
	policyErr   string
	allowed     bool
	message     string
	warnings    []string
	annotations map[string]string
	object      *unstructured.Unstructured
}

func (r serverResult) outcome() outcome {
	switch {
	case r.policyErr != "":
		return outcome{Result: resultError}
	case !r.allowed:
		return outcome{Result: resultDenied, Message: r.message, Warnings: r.warnings, AuditAnnotations: r.annotations}
	}

	out := outcome{Result: resultAllowed, Warnings: r.warnings, AuditAnnotations: r.annotations}
	if r.object != nil {
		out.Object = normalizeObject(r.object)
	}

	return out
}

// capture records the warnings and audit ID of the requests made with a
// config, which is created per case.
type capture struct {
	mu       sync.Mutex
	warnings []string
	auditIDs []string
}

func (c *capture) HandleWarningHeader(_ int, _ string, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.warnings = append(c.warnings, text)
}

func (c *capture) wrap(rt http.RoundTripper) http.RoundTripper {
	return roundTripper(func(req *http.Request) (*http.Response, error) {
		resp, err := rt.RoundTrip(req)
		if resp != nil {
			c.mu.Lock()
			c.auditIDs = append(c.auditIDs, resp.Header.Get("Audit-Id"))
			c.mu.Unlock()
		}

		return resp, err //nolint:wrapcheck // Transport errors pass through unchanged.
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// execute sends the case's request as the impersonated user and records the
// normalized server result, or why it is inconclusive.
func (h *shardRun) execute(ctx context.Context, c *katCase) {
	const (
		retries = 20
		backoff = 250 * time.Millisecond
	)

	for attempt := 0; ; attempt++ {
		capt := &capture{}
		obj, err := h.send(ctx, c, capt)

		if err != nil && strings.Contains(err.Error(), notSyncedMessage) && attempt < retries {
			time.Sleep(backoff)

			continue
		}

		if err := h.record(ctx, c, capt, obj, err); err != nil {
			c.inconclusivef("%v", err)
		}

		return
	}
}

func (h *shardRun) send(ctx context.Context, c *katCase, capt *capture) (*unstructured.Unstructured, error) {
	cfg := rest.CopyConfig(h.srv.cfg)
	cfg.WarningHandler = capt
	cfg.WrapTransport = capt.wrap
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: c.user.Name,
		UID:      c.user.UID,
		Groups:   c.user.Groups,
		Extra:    c.user.Extra,
	}

	if c.op == admissionv1.Connect {
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("create client: %w", err)
		}

		err = client.CoreV1().RESTClient().Post().
			Namespace(c.namespace).Resource("pods").Name(c.objName).SubResource("exec").
			VersionedParams(c.exec, scheme.ParameterCodec).
			Do(ctx).Error()

		return nil, err //nolint:wrapcheck // Classified by the caller.
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	ri := dyn.Resource(c.gvr).Namespace(c.namespace)
	if !c.namespaced {
		ri = dyn.Resource(c.gvr)
	}

	switch c.op {
	case admissionv1.Create:
		return ri.Create(ctx, c.run.send.DeepCopy(), metav1.CreateOptions{DryRun: dryRun()}) //nolint:wrapcheck // Classified by the caller.
	case admissionv1.Update:
		return ri.Update(ctx, c.run.send.DeepCopy(), metav1.UpdateOptions{DryRun: dryRun()}) //nolint:wrapcheck // Classified by the caller.
	case admissionv1.Delete, admissionv1.Connect:
	}

	return nil, ri.Delete(ctx, c.objName, metav1.DeleteOptions{DryRun: dryRun()}) //nolint:wrapcheck // Classified by the caller.
}

// record classifies the server's response. A denial by the policy under test
// is a decision; any other error is not, except for CONNECT, where the
// request fails after admission because the pod has no node.
func (h *shardRun) record(ctx context.Context, c *katCase, capt *capture, obj *unstructured.Unstructured, reqErr error) error {
	res := &c.run.server

	if reqErr == nil {
		res.allowed = true
		res.object = obj
	} else {
		var status apierrors.APIStatus
		if !errors.As(reqErr, &status) {
			return fmt.Errorf("request failed: %w", reqErr)
		}

		m := denialPattern.FindStringSubmatch(status.Status().Message)

		switch {
		case m != nil && (m[1] != c.policyName || m[2] != c.bindingName):
			return fmt.Errorf("denied by policy %q binding %q, not the policy under test: %w", m[1], m[2], reqErr)
		case m != nil:
			res.message = m[3]
		case c.op == admissionv1.Connect:
			res.allowed = true

			c.notef("CONNECT passed admission and then failed as expected: %v", reqErr)
		default:
			return fmt.Errorf("the server returned an error that is not an admission policy decision: %w", reqErr)
		}
	}

	res.warnings = c.policyWarnings(capt.warnings)

	return h.recordAudit(ctx, c, capt)
}

// policyWarnings keeps the Warn action warnings of the policy under test,
// without the server's prefix.
func (c *katCase) policyWarnings(warnings []string) []string {
	var out []string

	for _, w := range warnings {
		m := warningPattern.FindStringSubmatch(w)
		if m == nil || m[1] != c.policyName || m[2] != c.bindingName {
			c.notef("ignored server warning: %s", w)

			continue
		}

		out = append(out, m[3])
	}

	return out
}

// recordAudit reads the policy's audit annotations back from the audit log,
// without the "<policy>/" key prefix, and checks the identity the server saw.
func (h *shardRun) recordAudit(ctx context.Context, c *katCase, capt *capture) error {
	if len(capt.auditIDs) == 0 {
		return errNoAuditEvent
	}

	event, err := h.srv.auditEvent(ctx, capt.auditIDs[len(capt.auditIDs)-1])
	if err != nil {
		return err
	}

	prefix := c.policyName + "/"

	for k, v := range event.Annotations {
		if key, ok := strings.CutPrefix(k, prefix); ok {
			if c.run.server.annotations == nil {
				c.run.server.annotations = map[string]string{}
			}

			c.run.server.annotations[key] = v
		}
	}

	if seen := event.ImpersonatedUser; !sameIdentity(seen, c.user) {
		return fmt.Errorf("%w: server %+v, kat %+v", errIdentity, seen, c.user)
	}

	return nil
}

func sameIdentity(seen *auditUser, want *user.DefaultInfo) bool {
	return seen != nil && seen.Username == want.Name && seen.UID == want.UID &&
		slices.Equal(slices.Sorted(slices.Values(seen.Groups)), slices.Sorted(slices.Values(want.Groups))) &&
		maps.EqualFunc(seen.Extra, want.Extra, slices.Equal)
}
