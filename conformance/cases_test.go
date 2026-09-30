package conformance

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"sigs.k8s.io/yaml"

	"github.com/zemanlx/kat/internal/evaluator"
	"github.com/zemanlx/kat/internal/loader"
)

// defaultUser is the identity the harness uses, on both sides, for a case that
// sets no userInfo.
const defaultUser = "kat-conformance"

var errUnsupportedCase = errors.New("unsupported case")

// katCase is one fixture case resolved into a concrete admission request, plus
// everything the harness learns while running it.
type katCase struct {
	suiteID string
	name    string
	tc      *loader.TestCase

	mutatingPolicy    *admissionregv1.MutatingAdmissionPolicy
	mutatingBinding   *admissionregv1.MutatingAdmissionPolicyBinding
	validatingPolicy  *admissionregv1.ValidatingAdmissionPolicy
	validatingBinding *admissionregv1.ValidatingAdmissionPolicyBinding
	policyName        string
	bindingName       string

	// skip is set when the fixture cannot be turned into a request at all,
	// for example when it does not load.
	skip string

	op          admissionv1.Operation
	gvk         schema.GroupVersionKind
	gvr         schema.GroupVersionResource
	subresource string
	namespaced  bool
	namespace   string
	objName     string

	// object and oldObject are fixture copies with the request namespace set.
	object    *unstructured.Unstructured
	oldObject *unstructured.Unstructured
	exec      *corev1.PodExecOptions
	user      *user.DefaultInfo

	needs     []need
	paramsKey *objectKey
	rbac      rbacNeed

	run caseRun
}

// id is the case's name in known-divergences.yaml and in test output.
func (c *katCase) id() string { return c.suiteID + "/" + c.name }

// policy returns the policy under test's paramKind and failure policy.
func (c *katCase) paramKind() *admissionregv1.ParamKind {
	if c.mutatingPolicy != nil {
		return c.mutatingPolicy.Spec.ParamKind
	}

	return c.validatingPolicy.Spec.ParamKind
}

func (c *katCase) paramRef() *admissionregv1.ParamRef {
	switch {
	case c.mutatingBinding != nil:
		return c.mutatingBinding.Spec.ParamRef
	case c.validatingBinding != nil:
		return c.validatingBinding.Spec.ParamRef
	default:
		return nil
	}
}

// newCases resolves every test case of a suite against the server's API.
func newCases(suiteID string, suite *loader.TestSuite, mapper meta.RESTMapper) []*katCase {
	cases := make([]*katCase, 0, len(suite.Tests))

	for _, tc := range suite.Tests {
		c := &katCase{suiteID: suiteID, name: strings.TrimSuffix(tc.Name, ".yaml"), tc: tc}
		c.mutatingPolicy, c.mutatingBinding, c.validatingPolicy, c.validatingBinding = suite.FindPolicies(tc.PolicyName)

		switch {
		case tc.Error != nil:
			// kat itself reports the fixture as broken; there is no request to compare.
			c.skip = "fixture does not load: " + tc.Error.Error()
		default:
			if err := c.resolve(mapper); err != nil {
				c.inconclusivef("%v", err)
			} else if err := c.computeNeeds(mapper); err != nil {
				c.inconclusivef("%v", err)
			}
		}

		cases = append(cases, c)
	}

	return cases
}

func (c *katCase) resolve(mapper meta.RESTMapper) error {
	switch {
	case c.mutatingPolicy != nil:
		c.policyName = c.mutatingPolicy.Name
		if c.mutatingBinding != nil {
			c.bindingName = c.mutatingBinding.Name
		}
	case c.validatingPolicy != nil:
		c.policyName = c.validatingPolicy.Name
		if c.validatingBinding != nil {
			c.bindingName = c.validatingBinding.Name
		}
	default:
		return fmt.Errorf("%w: policy %q not found", errUnsupportedCase, c.tc.PolicyName)
	}

	if c.bindingName == "" {
		return fmt.Errorf("%w: policy %q has no binding, so the API server never evaluates it", errUnsupportedCase, c.policyName)
	}

	c.op = c.tc.Request.Operation

	if err := c.resolveResource(mapper); err != nil {
		return err
	}

	if err := c.resolveNamespace(); err != nil {
		return err
	}

	c.user = serverIdentity(c.tc.UserInfo)

	return nil
}

func (c *katCase) resolveResource(mapper meta.RESTMapper) error {
	if c.op == admissionv1.Connect {
		return c.resolveConnect()
	}

	described := c.tc.Object
	if described == nil {
		described = c.tc.OldObject
	}

	if described == nil {
		return fmt.Errorf("%w: %s without object or oldObject", errUnsupportedCase, c.op)
	}

	c.gvk = described.GroupVersionKind()

	mapping, err := mapper.RESTMapping(c.gvk.GroupKind(), c.gvk.Version)
	if err != nil {
		return fmt.Errorf("%w: the API server does not serve %s: %w", errUnsupportedCase, c.gvk, err)
	}

	c.gvr = mapping.Resource
	c.namespaced = mapping.Scope.Name() == meta.RESTScopeNameNamespace

	if c.tc.Object != nil {
		c.object = c.tc.Object.DeepCopy()
	}

	if c.tc.OldObject != nil {
		c.oldObject = c.tc.OldObject.DeepCopy()
	}

	c.objName = described.GetName()
	if c.objName == "" {
		c.objName = c.tc.Request.Name
	}

	if c.objName == "" {
		return fmt.Errorf("%w: object has no metadata.name", errUnsupportedCase)
	}

	return nil
}

// resolveConnect supports pods/exec, the only CONNECT subresource the harness
// can drive without a kubelet: admission runs before the proxy fails.
func (c *katCase) resolveConnect() error {
	req := c.tc.Request
	if req.Resource.Resource != "pods" || req.Resource.Group != "" || req.SubResource != "exec" {
		return fmt.Errorf("%w: CONNECT is only supported for pods/exec, got resource %q subResource %q",
			errUnsupportedCase, req.Resource.Resource, req.SubResource)
	}

	if req.Name == "" {
		return fmt.Errorf("%w: CONNECT request has no name", errUnsupportedCase)
	}

	c.gvr = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	c.gvk = schema.GroupVersionKind{Version: "v1", Kind: "PodExecOptions"}
	c.subresource = "exec"
	c.namespaced = true
	c.objName = req.Name
	c.exec = &corev1.PodExecOptions{}

	if req.Options.Raw != nil {
		if err := yaml.UnmarshalStrict(req.Options.Raw, c.exec); err != nil {
			return fmt.Errorf("%w: options are not PodExecOptions: %w", errUnsupportedCase, err)
		}
	}

	if c.exec.Container == "" {
		c.exec.Container = "app"
	}

	return nil
}

// resolveNamespace picks the request namespace the way a client would: the
// object's own namespace, then the request's, then the namespaceObject's name,
// then "default". A namespaceObject naming another namespace would give kat
// and the server different namespaces, so it is rejected.
func (c *katCase) resolveNamespace() error {
	nsObj := c.tc.NamespaceObj

	if !c.namespaced {
		if nsObj != nil {
			return fmt.Errorf("%w: namespaceObject is set for cluster-scoped %s", errUnsupportedCase, c.gvr.Resource)
		}

		return nil
	}

	candidates := []string{c.tc.Request.Namespace}
	for _, u := range []*unstructured.Unstructured{c.object, c.oldObject} {
		if u != nil {
			candidates = append([]string{u.GetNamespace()}, candidates...)
		}
	}

	if nsObj != nil {
		candidates = append(candidates, nsObj.GetName())
	}

	candidates = append(candidates, corev1.NamespaceDefault)

	c.namespace = candidates[slices.IndexFunc(candidates, func(s string) bool { return s != "" })]

	if nsObj != nil && nsObj.GetName() != c.namespace {
		return fmt.Errorf("%w: namespaceObject %q does not match the request namespace %q",
			errUnsupportedCase, nsObj.GetName(), c.namespace)
	}

	for _, u := range []*unstructured.Unstructured{c.object, c.oldObject} {
		if u != nil {
			u.SetNamespace(c.namespace)
		}
	}

	return nil
}

// serverIdentity returns the user the API server sees when the harness
// impersonates info: service accounts impersonated without groups get their
// service account groups, and every user gets system:authenticated.
func serverIdentity(info user.Info) *user.DefaultInfo {
	if info == nil {
		info = &user.DefaultInfo{Name: defaultUser}
	}

	identity := &user.DefaultInfo{
		Name:   info.GetName(),
		UID:    info.GetUID(),
		Groups: slices.Clone(info.GetGroups()),
		Extra:  info.GetExtra(),
	}

	if len(identity.Groups) == 0 {
		if ns, _, err := serviceaccount.SplitUsername(identity.Name); err == nil {
			identity.Groups = serviceaccount.MakeGroupNames(ns)
		}
	}

	if !slices.Contains(identity.Groups, user.AllAuthenticated) {
		identity.Groups = append(identity.Groups, user.AllAuthenticated)
	}

	return identity
}

// isMaster reports whether the identity bypasses RBAC.
func isMaster(u user.Info) bool {
	return slices.Contains(u.GetGroups(), user.SystemPrivilegedGroup)
}

type mockConfig = evaluator.AuthorizationMockConfig

// authorizerMocks returns the case's .authorizer.yaml entries.
func (c *katCase) authorizerMocks() []mockConfig {
	return c.tc.Authorizer
}
