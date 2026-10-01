package conformance

import (
	"errors"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/zemanlx/kat/internal/evaluator"
)

// Outcome results. A policy the server rejects at creation is compared with
// kat rejecting it with the same error; any other kat error never matches.
const (
	resultAllowed  = "allowed"
	resultDenied   = "denied"
	resultRejected = "rejected"
	resultError    = "error"
)

// outcome is what both sides are normalized to before they are compared.
type outcome struct {
	Result           string              `json:"result"`
	Message          string              `json:"message,omitempty"`
	Binding          string              `json:"binding,omitempty"`
	Reason           metav1.StatusReason `json:"reason,omitempty"`
	Warnings         []string            `json:"warnings,omitempty"`
	AuditAnnotations map[string]string   `json:"auditAnnotations,omitempty"`
	Object           map[string]any      `json:"object,omitempty"`
}

// caseRun is everything the harness computes for one case.
type caseRun struct {
	notes    []string
	problems []string

	// Inputs both sides evaluate.
	request      *admissionv1.AdmissionRequest
	object       *unstructured.Unstructured
	oldObject    *unstructured.Unstructured
	namespaceObj *unstructured.Unstructured
	params       *unstructured.Unstructured
	// send is the body of the CREATE or UPDATE request sent to the server.
	send *unstructured.Unstructured

	katResult    *evaluator.EvaluationResult
	katErr       error
	katObject    map[string]any
	katObjectErr string

	server serverResult

	kat, srv outcome
	diff     string

	binaryRan     bool
	binaryFailure string
}

// inconclusive records why the case could not be compared: the harness or
// the server could not run it as a policy decision.
func (c *katCase) inconclusivef(format string, args ...any) {
	c.run.problems = append(c.run.problems, "inconclusive: "+fmt.Sprintf(format, args...))
}

func (c *katCase) notef(format string, args ...any) {
	c.run.notes = append(c.run.notes, fmt.Sprintf(format, args...))
}

func (c *katCase) isInconclusive() bool { return len(c.run.problems) > 0 }

// katOutcome normalizes kat's raw evaluation result.
func katOutcome(res *evaluator.EvaluationResult, err error, object map[string]any, objectErr string) outcome {
	if invalid, ok := errors.AsType[*evaluator.InvalidPolicyError](err); ok {
		return outcome{Result: resultRejected, Message: invalid.Error()}
	}

	switch {
	case err != nil:
		return outcome{Result: resultError, Message: err.Error()}
	case !res.Allowed:
		return outcome{
			Result:           resultDenied,
			Message:          res.Message,
			Binding:          res.Binding,
			Reason:           res.Reason,
			Warnings:         res.Warnings,
			AuditAnnotations: res.AuditAnnotations,
		}
	}

	out := outcome{Result: resultAllowed, Warnings: res.Warnings, AuditAnnotations: res.AuditAnnotations, Object: object}
	if objectErr != "" {
		out.Object = map[string]any{"error": objectErr}
	}

	return out
}

// compare runs the strict library-level diff of kat against the server.
func (c *katCase) compare() {
	run := &c.run
	run.kat = katOutcome(run.katResult, run.katErr, run.katObject, run.katObjectErr)
	run.srv = run.server.outcome()
	run.diff = cmp.Diff(run.srv, run.kat, cmpopts.EquateEmpty())
}

// layer1Failures reports the library-level comparison.
func (c *katCase) layer1Failures() []string {
	if c.isInconclusive() {
		return c.run.problems
	}

	if c.run.diff == "" {
		return nil
	}

	return []string{"kat differs from the kube-apiserver (-server +kat):\n" + c.run.diff}
}

// withoutServerFields drops the metadata the API server sets rather than a
// client or a policy.
func withoutServerFields(u *unstructured.Unstructured) *unstructured.Unstructured {
	out := u.DeepCopy()
	for _, f := range []string{"uid", "resourceVersion", "creationTimestamp", "generation", "managedFields", "selfLink"} {
		unstructured.RemoveNestedField(out.Object, "metadata", f)
	}

	return out
}

func withoutManagedFields(u *unstructured.Unstructured) *unstructured.Unstructured {
	out := u.DeepCopy()
	unstructured.RemoveNestedField(out.Object, "metadata", "managedFields")

	return out
}

// normalizeObject keeps only what a mutation can meaningfully change. Both
// sides went through the server's dry-run, so status is comparable.
func normalizeObject(u *unstructured.Unstructured) map[string]any {
	return withoutServerFields(u).Object
}
