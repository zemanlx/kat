package evaluator

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	admissionregv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/apiserver/pkg/admission"
	plugincel "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/mutating/patch"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/matchconditions"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/cel/environment"

	"github.com/zemanlx/kat/internal/ssa"
)

// mutator is a compiled MutatingAdmissionPolicy, as built by the API server's
// mutating.compilePolicy.
type mutator struct {
	policy    *admissionregv1.MutatingAdmissionPolicy
	compiler  *plugincel.CompositedCompiler
	matcher   matchconditions.Matcher
	patchers  []patch.Patcher
	converter managedfields.TypeConverter
}

func (e *Evaluator) compileMutating(policy *admissionregv1.MutatingAdmissionPolicy) (*mutator, error) {
	compiler, err := plugincel.NewCompositedCompiler(e.env)
	if err != nil {
		return nil, fmt.Errorf("create CEL compiler: %w", err)
	}

	spec := &policy.Spec
	opts := plugincel.OptionalVariableDeclarations{HasParams: spec.ParamKind != nil, HasAuthorizer: true}

	variables := make([]plugincel.NamedExpressionAccessor, len(spec.Variables))
	for i, v := range spec.Variables {
		variables[i] = &namedExpression{name: v.Name, expression: v.Expression}
	}

	compiler.CompileAndStoreVariables(variables, opts, environment.StoredExpressions)

	converter, err := ssa.TypeConverter()
	if err != nil {
		return nil, fmt.Errorf("create type converter: %w", err)
	}

	m := &mutator{policy: policy, compiler: compiler, converter: converter}

	if len(spec.MatchConditions) > 0 {
		conditions := make([]plugincel.ExpressionAccessor, len(spec.MatchConditions))
		for i := range spec.MatchConditions {
			conditions[i] = (*matchconditions.MatchCondition)(&spec.MatchConditions[i])
		}

		m.matcher = matchconditions.NewMatcher(compiler.CompileCondition(conditions, opts, environment.StoredExpressions),
			spec.FailurePolicy, "policy", "validate", policy.Name)
	}

	m.patchers = compilePatchers(compiler, spec.Mutations, opts)

	return m, nil
}

func compilePatchers(
	compiler *plugincel.CompositedCompiler,
	mutations []admissionregv1.Mutation,
	opts plugincel.OptionalVariableDeclarations,
) []patch.Patcher {
	opts.HasPatchTypes = true

	patchers := make([]patch.Patcher, 0, len(mutations))

	for _, mutation := range mutations {
		switch {
		case mutation.PatchType == admissionregv1.PatchTypeJSONPatch && mutation.JSONPatch != nil:
			accessor := &patch.JSONPatchCondition{Expression: mutation.JSONPatch.Expression}
			patchers = append(patchers, typedJSONPatcher{patch.NewJSONPatcher(compiler.CompileMutatingEvaluator(accessor, opts, environment.StoredExpressions))})
		case mutation.PatchType == admissionregv1.PatchTypeApplyConfiguration && mutation.ApplyConfiguration != nil:
			accessor := &patch.ApplyConfigurationCondition{Expression: mutation.ApplyConfiguration.Expression}
			patchers = append(patchers, patch.NewApplyConfigurationPatcher(compiler.CompileMutatingEvaluator(accessor, opts, environment.StoredExpressions)))
		}
	}

	return patchers
}

// typedJSONPatcher is a JSON patcher whose result for a built-in kind must
// decode strictly into the typed object, as on the API server, which patches
// typed objects. kat patches unstructured ones, which accept any field.
type typedJSONPatcher struct {
	patch.Patcher
}

func (p typedJSONPatcher) Patch(ctx context.Context, r patch.Request, runtimeCELCostBudget int64) (runtime.Object, error) {
	patched, err := p.Patcher.Patch(ctx, r, runtimeCELCostBudget)
	if err != nil {
		return nil, err //nolint:wrapcheck // The server's error, unchanged.
	}

	// A failed "test" operation returns the object unpatched, and the server
	// does not decode it.
	u, ok := patched.(*unstructured.Unstructured)
	if !ok || patched == r.VersionedAttributes.VersionedObject.Object() {
		return patched, nil
	}

	if err := decodeTyped(u); err != nil {
		return nil, apierrors.NewInternalError(err)
	}

	return patched, nil
}

// policyError is the API server's generic.PolicyError for one binding.
type policyError struct {
	binding string
	err     error
	reason  metav1.StatusReason
}

type invocation struct {
	binding string
	params  runtime.Object
}

//nolint:cyclop // The fatal params error is an extra branch the server answers by listing.
func (e *Evaluator) evaluateMutating(
	ctx context.Context,
	policy *admissionregv1.MutatingAdmissionPolicy,
	bindings []*admissionregv1.MutatingAdmissionPolicyBinding,
	req *request,
) (*EvaluationResult, error) {
	m, err := e.compileMutating(policy)
	if err != nil {
		return nil, err
	}

	invocations, errs, err := mutatingInvocations(policy, bindings, req)
	if err != nil {
		return nil, err
	}

	original := req.attr.VersionedObject.Object()
	ignore := failurePolicy(policy.Spec.FailurePolicy) == admissionregv1.Ignore

	for pass := 0; pass < 2 && len(invocations) > 0; pass++ {
		if pass == 1 && !m.reinvoked(req, len(errs) == 0 || ignore) {
			break
		}

		dispatchErrs, statusErr := m.dispatch(ctx, req, invocations)
		if statusErr != nil {
			return &EvaluationResult{Message: statusErr.Error(), Reason: statusErr.ErrStatus.Reason}, nil
		}

		errs = append(errs, dispatchErrs...)
	}

	if len(errs) > 0 && !ignore {
		first := errs[0]

		return &EvaluationResult{Message: first.err.Error(), Binding: first.binding, Reason: cmp.Or(first.reason, metav1.StatusReasonForbidden)}, nil
	}

	return admitted(original, req.attr.VersionedObject.Object()), nil
}

// admitted is the result of a request the policy admitted, with the final
// object if the policy changed it.
func admitted(original, final runtime.Object) *EvaluationResult {
	result := &EvaluationResult{Allowed: true}

	// JSON equality, as the patchers turn the input's float64 numbers into int64.
	if u, ok := final.(*unstructured.Unstructured); ok && !jsonEqual(original, u) {
		result.PatchedObject = u
	}

	return result
}

// reinvoked reports whether the server runs the policy once more: it does when
// reinvocationPolicy is IfNeeded and a pass that did not deny the request
// changed the object.
func (m *mutator) reinvoked(req *request, admitted bool) bool {
	return m.policy.Spec.ReinvocationPolicy == admissionregv1.IfNeededReinvocationPolicy && req.attr.Dirty && admitted
}

// mutatingInvocations returns the policy's invocations for the request, one
// per binding that matches it and param it selects, and the bindings' errors.
func mutatingInvocations(
	policy *admissionregv1.MutatingAdmissionPolicy,
	bindings []*admissionregv1.MutatingAdmissionPolicyBinding,
	req *request,
) ([]invocation, []policyError, error) {
	var (
		invocations []invocation
		errs        []policyError
	)

	for _, binding := range bindings {
		applies, err := policyApplies(policy.Spec.MatchConstraints, binding.Spec.MatchResources, req.attr, req.namespaceObj)
		if err == nil && applies {
			var params []runtime.Object

			params, err = collectParams(policy.Spec.ParamKind, binding.Spec.ParamRef, req.params, req.attr.GetNamespace())
			for _, p := range params {
				invocations = append(invocations, invocation{binding.Name, p})
			}
		}

		if fatalParamsErr(err) {
			return nil, nil, err
		}

		if err != nil {
			errs = append(errs, policyError{binding: binding.Name, err: fmt.Errorf("failed to configure binding: %w", err)})
		}
	}

	return invocations, errs, nil
}

// dispatch is one pass of the API server's mutating dispatcher over the
// policy's invocations.
func (m *mutator) dispatch(
	ctx context.Context,
	req *request,
	invocations []invocation,
) ([]policyError, *apierrors.StatusError) {
	var errs []policyError

	objectInterfaces := admission.NewObjectInterfacesFromScheme(runtime.NewScheme())

	for _, inv := range invocations {
		compositionCtx := m.compiler.CreateContext(ctx)

		if m.matcher != nil {
			match := m.matcher.Match(compositionCtx, req.attr, inv.params, req.authz) //nolint:contextcheck // Wraps ctx.
			if match.Error != nil {
				errs = append(errs, policyError{binding: inv.binding, err: match.Error, reason: metav1.StatusReasonInvalid})

				continue
			}

			if !match.Matches {
				continue
			}
		}

		for _, patcher := range m.patchers {
			if req.attr.VersionedObject.Object() == nil {
				continue
			}

			patched, err := patcher.Patch(compositionCtx, patch.Request{ //nolint:contextcheck // Wraps ctx.
				MatchedResource:     req.attr.GetResource(),
				VersionedAttributes: req.attr,
				ObjectInterfaces:    objectInterfaces,
				OptionalVariables:   plugincel.OptionalVariableBindings{VersionedParams: inv.params, Authorizer: req.authz},
				Namespace:           req.namespace,
				TypeConverter:       m.converter,
			}, celconfig.RuntimeCELCostBudget)
			if err != nil {
				if statusErr, ok := errors.AsType[*apierrors.StatusError](err); ok {
					return nil, statusErr
				}

				errs = append(errs, policyError{binding: inv.binding, err: err, reason: metav1.StatusReasonInvalid})

				continue
			}

			req.attr.UpdateObject(patched)
		}
	}

	return errs, nil
}
