package evaluator

import (
	"cmp"
	"errors"
	"fmt"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apiserver/pkg/admission"
	plugincel "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/cel/environment"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/yaml"
)

// DefaultKubernetesVersion is the newest kube-apiserver minor version kat
// behaves like, the version of its k8s.io libraries.
const DefaultKubernetesVersion = "1.37"

const (
	minKubernetesMinor = 36
	maxKubernetesMinor = 37
)

var (
	errUnsupportedVersion = errors.New("unsupported Kubernetes version")
	errParamsNotFound     = errors.New("no params found for policy binding with `Deny` parameterNotFoundAction")
	// ErrParamSelectorUnsupported is returned when a binding selects params with
	// a selector. kat has no cluster to list, so it does not guess.
	ErrParamSelectorUnsupported = errors.New("selector paramRefs are not supported offline")
	// ErrParamNameMismatch is returned when the fixture params object is named
	// and that name is not paramRef.name.
	ErrParamNameMismatch = errors.New("params object name does not match paramRef.name")
	// ErrParamNamespaceMismatch is returned when the fixture params object has a
	// namespace other than the one the server looks params up in: paramRef's
	// namespace, else the request's.
	ErrParamNamespaceMismatch = errors.New("params object namespace is not where paramRef looks")
)

// Evaluator evaluates admission policies the way a kube-apiserver of one
// minor version does.
type Evaluator struct {
	// env is the API server's base CEL environment. It compiles a policy's
	// expressions as the server does at creation (NewExpressions), which
	// accepts the features of the previous minor version so a rollback keeps
	// working, and when it admits a request (StoredExpressions).
	env *environment.EnvSet
	// statelessCompiler compiles the expressions that cannot reference
	// variables. It is costly to build, so it is shared.
	statelessCompiler plugincel.Compiler
}

// New creates an Evaluator that behaves like the DefaultKubernetesVersion API server.
func New() (*Evaluator, error) {
	return NewForVersion(DefaultKubernetesVersion)
}

// NewForVersion creates an Evaluator that validates new policies the way the
// API server of the given major.minor version does. The compatibility version
// is one minor below that, which is what filters NewExpressions, the
// creation-time CEL feature set. Admission evaluates with StoredExpressions:
// every feature the linked k8s.io/apiserver knows. That set cannot be
// downgraded. Structural validation is the linked version's rules as well.
func NewForVersion(kubernetesVersion string) (*Evaluator, error) {
	v, err := version.ParseMajorMinor(kubernetesVersion)
	if err != nil || v.Major() != 1 || v.Minor() < minKubernetesMinor || v.Minor() > maxKubernetesMinor {
		return nil, fmt.Errorf("%w %q: kat supports 1.%d to 1.%d", errUnsupportedVersion, kubernetesVersion, minKubernetesMinor, maxKubernetesMinor)
	}

	// The server's environment.DefaultCompatibilityVersion is its minimum
	// compatibility version, one minor below its own.
	env := environment.MustBaseEnvSet(version.MajorMinor(1, v.Minor()-1))

	return &Evaluator{env: env, statelessCompiler: plugincel.NewCompiler(env)}, nil
}

// policyCompiler returns a compiler for one policy's expressions. Variable
// composition needs a compiler of its own. A failure is returned to the
// caller; it is not replaced with the stateless compiler.
func (e *Evaluator) policyCompiler(composition bool) (plugincel.Compiler, error) {
	if !composition {
		return e.statelessCompiler, nil
	}

	compiler, err := plugincel.NewCompositedCompiler(e.env)
	if err != nil {
		return nil, fmt.Errorf("create CEL compiler: %w", err)
	}

	return compiler, nil
}

// request is a test case's admission request as an admission plugin sees it.
type request struct {
	attr *admission.VersionedAttributes
	// namespace is the request's Namespace, nil for a cluster-scoped request
	// or when the test case does not describe it.
	namespace    *corev1.Namespace
	namespaceObj *unstructured.Unstructured
	params       *unstructured.Unstructured
	authz        authorizer.Authorizer
}

func newRequest(
	req *admissionv1.AdmissionRequest,
	object, oldObject, params, namespaceObj *unstructured.Unstructured,
	authz authorizer.Authorizer,
	userInfo user.Info,
) (*request, error) {
	if req == nil {
		req = &admissionv1.AdmissionRequest{}
	}

	if err := errors.Join(checkInput("object", object), checkInput("oldObject", oldObject)); err != nil {
		return nil, err
	}

	attr, err := newAttributes(req, object, oldObject, namespaceObj, userInfo)
	if err != nil {
		return nil, err
	}

	versioned := &admission.VersionedAttributes{Attributes: attr, VersionedKind: attr.GetKind()}
	if obj := attr.GetObject(); obj != nil {
		versioned.VersionedObject.Set(obj)
	}

	if old := attr.GetOldObject(); old != nil {
		versioned.VersionedOldObject.Set(old)
	}

	r := &request{
		attr:         versioned,
		namespaceObj: namespaceObj,
		params:       params,
		authz:        authz,
	}

	// The server always has an authorizer; without mocks nothing is allowed.
	if r.authz == nil {
		r.authz = NewMockAuthorizer()
	}

	r.namespace, err = requestNamespace(attr, namespaceObj)
	if err != nil {
		return nil, err
	}

	return r, nil
}

// requestNamespace is the namespace the server evaluates the request in.
func requestNamespace(attr admission.Attributes, namespaceObj *unstructured.Unstructured) (*corev1.Namespace, error) {
	// Like the server, a Namespace request is evaluated without a namespace.
	if gvk := attr.GetKind(); gvk.Group == "" && gvk.Version == "v1" && gvk.Kind == "Namespace" {
		return nil, nil //nolint:nilnil // No namespace is not an error.
	}

	if namespaceObj == nil || attr.GetNamespace() == "" {
		return nil, nil //nolint:nilnil // No namespace is not an error.
	}

	namespace := &corev1.Namespace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(namespaceObj.Object, namespace); err != nil {
		return nil, fmt.Errorf("convert namespaceObject: %w", err)
	}

	return namespace, nil
}

// newAttributes adapts a test's admission request to admission.Attributes. When
// the request carries no namespace, the namespaceObject's name stands in so that
// rule scope sees the request as namespaced.
func newAttributes(
	req *admissionv1.AdmissionRequest,
	object, oldObject, namespaceObj *unstructured.Unstructured,
	userInfo user.Info,
) (admission.Attributes, error) {
	namespace := req.Namespace
	if namespace == "" && namespaceObj != nil {
		namespace = namespaceObj.GetName()
	}

	var options runtime.Object

	if req.Options.Raw != nil {
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(req.Options.Raw, &u.Object); err != nil {
			return nil, fmt.Errorf("unmarshal options: %w", err)
		}

		options = u
	}

	if userInfo == nil {
		userInfo = requestUser(req)
	}

	return admission.NewAttributesRecord(
		asRuntimeObject(object.DeepCopy()),
		asRuntimeObject(oldObject.DeepCopy()),
		schema.GroupVersionKind{Group: req.Kind.Group, Version: req.Kind.Version, Kind: req.Kind.Kind},
		namespace,
		req.Name,
		schema.GroupVersionResource{Group: req.Resource.Group, Version: req.Resource.Version, Resource: req.Resource.Resource},
		req.SubResource,
		admission.Operation(req.Operation),
		options,
		req.DryRun != nil && *req.DryRun,
		userInfo,
	), nil
}

func requestUser(req *admissionv1.AdmissionRequest) user.Info {
	info := &user.DefaultInfo{
		Name:   req.UserInfo.Username,
		UID:    req.UserInfo.UID,
		Groups: req.UserInfo.Groups,
	}

	if len(req.UserInfo.Extra) > 0 {
		info.Extra = make(map[string][]string, len(req.UserInfo.Extra))
		for k, v := range req.UserInfo.Extra {
			info.Extra[k] = v
		}
	}

	return info
}

// decodeTyped decodes u strictly into the typed object of its built-in kind.
// A kind without a Go type, such as a custom resource, stays unstructured on
// the server as well, and is not checked.
func decodeTyped(u *unstructured.Unstructured) error {
	typed, err := scheme.Scheme.New(u.GroupVersionKind())
	if err != nil {
		return nil //nolint:nilerr // Not a built-in kind.
	}

	js, err := u.MarshalJSON()
	if err != nil {
		return fmt.Errorf("encode object: %w", err)
	}

	decoder := json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme, json.SerializerOptions{Strict: true})
	_, _, err = decoder.Decode(js, nil, typed)

	return err //nolint:wrapcheck // The server's error, unchanged.
}

// checkInput rejects an input object that the API server would not pass to
// admission as written. With the default fieldValidation (Warn) it drops
// unknown fields, and it rejects a field of the wrong type.
func checkInput(name string, u *unstructured.Unstructured) error {
	if u == nil {
		return nil
	}

	err := decodeTyped(u)

	switch {
	case err == nil:
		return nil
	case runtime.IsStrictDecodingError(err):
		return fmt.Errorf("%s has a field the API server drops before admission: %w", name, err)
	default:
		return fmt.Errorf("the API server would reject the %s: %w", name, err)
	}
}

// asRuntimeObject keeps a nil *Unstructured from becoming a non-nil interface.
func asRuntimeObject(u *unstructured.Unstructured) runtime.Object {
	if u == nil {
		return nil
	}

	return u
}

// collectParams is the server's generic.CollectParams for a test case, which
// has at most one params object. No params means the binding is skipped.
func collectParams(
	paramKind *admissionregv1.ParamKind,
	paramRef *admissionregv1.ParamRef,
	params *unstructured.Unstructured,
	namespace string,
) ([]runtime.Object, error) {
	if err := checkParamRef(paramKind, paramRef, params, namespace); err != nil {
		return nil, err
	}

	switch {
	case paramKind == nil || paramRef == nil:
		return []runtime.Object{nil}, nil
	case params != nil:
		if params.GroupVersionKind().Empty() {
			params = params.DeepCopy()
			params.SetAPIVersion(paramKind.APIVersion)
			params.SetKind(paramKind.Kind)
		}

		return []runtime.Object{params}, nil
	case paramRef.ParameterNotFoundAction != nil && *paramRef.ParameterNotFoundAction == admissionregv1.DenyAction:
		return nil, errParamsNotFound
	}

	return nil, nil
}

// checkParamRef rejects a selector, and a named or namespaced fixture that is
// not where paramRef looks. kat cannot list a cluster, and it does not use the
// wrong object. kat does not know the paramKind's scope, so a fixture without
// a namespace, or a request without one, is not checked.
func checkParamRef(paramKind *admissionregv1.ParamKind, paramRef *admissionregv1.ParamRef, params *unstructured.Unstructured, namespace string) error {
	if paramKind == nil || paramRef == nil {
		return nil
	}

	if paramRef.Selector != nil && paramRef.Name == "" {
		return ErrParamSelectorUnsupported
	}

	if params == nil {
		return nil
	}

	if paramRef.Name != "" && params.GetName() != "" && params.GetName() != paramRef.Name {
		return fmt.Errorf("%w: paramRef.name is %q, params object is %q", ErrParamNameMismatch, paramRef.Name, params.GetName())
	}

	return checkParamsNamespace(cmp.Or(paramRef.Namespace, namespace), params)
}

// checkParamsNamespace rejects a namespaced params fixture that is not in the
// namespace the server looks params up in.
func checkParamsNamespace(namespace string, params *unstructured.Unstructured) error {
	if namespace == "" || params.GetNamespace() == "" || params.GetNamespace() == namespace {
		return nil
	}

	return fmt.Errorf("%w: params are looked up in namespace %q, params object is in %q", ErrParamNamespaceMismatch, namespace, params.GetNamespace())
}

// fatalParamsErr reports a params error kat cannot turn into an admission
// decision. failurePolicy must not swallow it.
func fatalParamsErr(err error) bool {
	return errors.Is(err, ErrParamSelectorUnsupported) || errors.Is(err, ErrParamNameMismatch) || errors.Is(err, ErrParamNamespaceMismatch)
}

// failurePolicy is the policy's failurePolicy, which the server defaults to Fail.
func failurePolicy(fp *admissionregv1.FailurePolicyType) admissionregv1.FailurePolicyType {
	if fp == nil {
		return admissionregv1.Fail
	}

	return *fp
}
