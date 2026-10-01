package conformance

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/zemanlx/kat/internal/evaluator"
)

// objectKey identifies one stored object.
type objectKey struct {
	resource  schema.GroupVersionResource
	namespace string
	name      string
}

func (k objectKey) String() string {
	if k.namespace == "" {
		return k.resource.Resource + "/" + k.name
	}

	return k.resource.Resource + "/" + k.namespace + "/" + k.name
}

// need is a precondition on server state that a case relies on: the object
// must be absent, or present with the given content. A loose need accepts any
// content and only supplies a default to create.
type need struct {
	key     objectKey
	present bool
	object  *unstructured.Unstructured
	loose   bool
}

// merge combines two needs on the same key, or reports that no single server
// can satisfy both.
func (n need) merge(other need) (need, bool) {
	switch {
	case n.present != other.present:
		return need{}, false
	case !n.present, other.loose:
		return n, true
	case n.loose:
		return other, true
	default:
		return n, reflect.DeepEqual(n.object.Object, other.object.Object)
	}
}

// rbacRule is one permission, in the shape of an authorizer check.
type rbacRule struct {
	group       string
	resource    string
	subresource string
	namespace   string
	verb        string
}

func (r rbacRule) String() string {
	res := r.resource
	if r.subresource != "" {
		res += "/" + r.subresource
	}

	scope := "cluster-wide"
	if r.namespace != "" {
		scope = "in namespace " + r.namespace
	}

	return fmt.Sprintf("%s %s.%s %s", r.verb, res, r.group, scope)
}

// covers reports whether granting r also allows other.
func (r rbacRule) covers(other rbacRule) bool {
	return r.group == other.group && r.resource == other.resource && r.subresource == other.subresource &&
		r.verb == other.verb && (r.namespace == "" || r.namespace == other.namespace)
}

// rbacNeed is what one user must, and must not, be allowed to do.
type rbacNeed struct {
	user   *user.DefaultInfo
	grants []rbacRule
	denies []rbacRule
	// allowMocks are the grants that come from Allow authorizer mocks. kat
	// answers an unmocked check with NoOpinion, so cases of one user share a
	// server only when they mock the same Allow checks.
	allowMocks []rbacRule
}

func sameRules(a, b []rbacRule) bool {
	set := func(rules []rbacRule) map[rbacRule]bool {
		m := make(map[rbacRule]bool, len(rules))
		for _, r := range rules {
			m[r] = true
		}

		return m
	}

	return maps.Equal(set(a), set(b))
}

func (n rbacNeed) conflict() (rbacRule, rbacRule, bool) {
	for _, deny := range n.denies {
		for _, grant := range n.grants {
			if grant.covers(deny) {
				return grant, deny, true
			}
		}
	}

	return rbacRule{}, rbacRule{}, false
}

func namespacesGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
}

// computeNeeds lists the server state and permissions the case relies on.
func (c *katCase) computeNeeds(mapper meta.RESTMapper) error {
	if c.namespaced {
		c.needs = append(c.needs, namespaceNeed(c.namespace, c.tc.NamespaceObj))
	}

	key := objectKey{resource: c.gvr, namespace: c.namespace, name: c.objName}

	switch c.op {
	case admissionv1.Create:
		c.needs = append(c.needs, need{key: key, present: false})
	case admissionv1.Update, admissionv1.Delete:
		c.needs = append(c.needs, storedNeed(key, c.oldObject))
	case admissionv1.Connect:
		c.needs = append(c.needs, need{key: key, present: true, loose: true, object: execPod(c)})
	}

	paramsNeed, err := c.paramsNeed(mapper)
	if err != nil {
		return err
	}

	if paramsNeed != nil {
		c.needs = append(c.needs, *paramsNeed)

		if paramsNeed.present {
			c.paramsKey = &paramsNeed.key
		}
	}

	return c.computeRBAC()
}

// namespaceNeed requires the namespace to exist, with the fixture's labels and
// annotations when the case defines its namespaceObject.
func namespaceNeed(name string, fixture *unstructured.Unstructured) need {
	ns := newNamespace(name)
	if fixture == nil {
		return need{key: objectKey{resource: namespacesGVR(), name: name}, present: true, loose: true, object: ns}
	}

	if labels := fixture.GetLabels(); len(labels) > 0 {
		ns.SetLabels(labels)
	}

	if annotations := fixture.GetAnnotations(); len(annotations) > 0 {
		ns.SetAnnotations(annotations)
	}

	return need{key: objectKey{resource: namespacesGVR(), name: name}, present: true, object: ns}
}

// storedNeed requires the oldObject of an UPDATE or DELETE to be stored. A
// stored Namespace only needs its labels and annotations to agree.
func storedNeed(key objectKey, oldObject *unstructured.Unstructured) need {
	if key.resource == namespacesGVR() {
		return namespaceNeed(key.name, oldObject)
	}

	return need{key: key, present: true, object: withoutServerFields(oldObject)}
}

func newNamespace(name string) *unstructured.Unstructured {
	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(name)

	return ns
}

// execPod is the Pod a CONNECT case execs into. Admission runs before the
// kubelet proxy, so it never needs to be scheduled.
func execPod(c *katCase) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": c.objName, "namespace": c.namespace},
		"spec": map[string]any{
			"containers": []any{map[string]any{"name": c.exec.Container, "image": "busybox"}},
		},
	}}
}

// paramsNeed seeds the fixture's params, or, when the binding references
// params the fixture does not provide, requires them to be absent.
func (c *katCase) paramsNeed(mapper meta.RESTMapper) (*need, error) {
	paramKind := c.paramKind()
	params := c.tc.Params

	paramRef, err := c.paramsRef(params)
	if err != nil {
		return nil, err
	}

	if params == nil && (paramKind == nil || paramRef == nil || paramRef.Name == "") {
		return nil, nil //nolint:nilnil // No params involved.
	}

	mapping, err := paramsMapping(mapper, params, paramKind)
	if err != nil {
		return nil, err
	}

	namespace := c.paramsNamespace(mapping, paramRef)

	if params == nil {
		return &need{key: objectKey{resource: mapping.Resource, namespace: namespace, name: paramRef.Name}}, nil
	}

	if err := paramsInNamespace(mapping, params, namespace); err != nil {
		return nil, err
	}

	return c.seedParams(mapping, params, namespace), nil
}

// paramsInNamespace rejects a params fixture that is not where the server
// looks params up (kat rejects it too), and a namespace on cluster-scoped
// params.
func paramsInNamespace(mapping *meta.RESTMapping, params *unstructured.Unstructured, namespace string) error {
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace && params.GetNamespace() != "" {
		return fmt.Errorf("%w: params of cluster-scoped %s have namespace %q", errUnsupportedCase, mapping.GroupVersionKind.Kind, params.GetNamespace())
	}

	if namespace == "" || params.GetNamespace() == "" || params.GetNamespace() == namespace {
		return nil
	}

	return fmt.Errorf("%w: params are looked up in namespace %q, params object is in %q: %w",
		errUnsupportedCase, namespace, params.GetNamespace(), evaluator.ErrParamNamespaceMismatch)
}

// paramsRef returns the binding's paramRef, or an error when kat cannot
// resolve it offline: a selector, or a fixture name that is not paramRef.name.
func (c *katCase) paramsRef(params *unstructured.Unstructured) (*admissionregv1.ParamRef, error) {
	paramRef, err := c.paramRef()
	if err != nil {
		return nil, err
	}

	if paramRef != nil && paramRef.Selector != nil && paramRef.Name == "" {
		return nil, fmt.Errorf("%w: %w", errUnsupportedCase, evaluator.ErrParamSelectorUnsupported)
	}

	if params != nil && paramRef != nil && paramRef.Name != "" && params.GetName() != "" && params.GetName() != paramRef.Name {
		return nil, fmt.Errorf("%w: paramRef.name is %q, params object is %q: %w",
			errUnsupportedCase, paramRef.Name, params.GetName(), evaluator.ErrParamNameMismatch)
	}

	return paramRef, nil
}

// seedParams requires the fixture's params to exist, by default in namespace.
func (c *katCase) seedParams(mapping *meta.RESTMapping, params *unstructured.Unstructured, namespace string) *need {
	seeded := withoutServerFields(params)
	if seeded.GetNamespace() == "" {
		seeded.SetNamespace(namespace)
	}

	if seeded.GetNamespace() != "" && seeded.GetNamespace() != c.namespace {
		c.needs = append(c.needs, namespaceNeed(seeded.GetNamespace(), nil))
	}

	key := objectKey{resource: mapping.Resource, namespace: seeded.GetNamespace(), name: seeded.GetName()}

	return &need{key: key, present: true, object: seeded}
}

// paramsNamespace is where the server looks params up: the paramRef's
// namespace, else the request's, for namespaced params.
func (c *katCase) paramsNamespace(mapping *meta.RESTMapping, paramRef *admissionregv1.ParamRef) string {
	switch {
	case mapping.Scope.Name() != meta.RESTScopeNameNamespace:
		return ""
	case paramRef != nil && paramRef.Namespace != "":
		return paramRef.Namespace
	default:
		return c.namespace
	}
}

// paramsMapping maps the fixture's params, or else the policy's paramKind,
// to a served resource.
func paramsMapping(mapper meta.RESTMapper, params *unstructured.Unstructured, paramKind *admissionregv1.ParamKind) (*meta.RESTMapping, error) {
	var gvk schema.GroupVersionKind

	if params != nil {
		gvk = params.GroupVersionKind()
	} else {
		gv, err := schema.ParseGroupVersion(paramKind.APIVersion)
		if err != nil {
			return nil, fmt.Errorf("%w: paramKind apiVersion %q: %w", errUnsupportedCase, paramKind.APIVersion, err)
		}

		gvk = gv.WithKind(paramKind.Kind)
	}

	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("%w: params kind %s is not served: %w", errUnsupportedCase, gvk, err)
	}

	return mapping, nil
}

// computeRBAC grants the impersonated user the request itself and every
// Allow authorizer mock, and records Deny mocks, which must stay ungranted.
func (c *katCase) computeRBAC() error {
	c.rbac = rbacNeed{user: c.user}
	mocks := c.tc.Authorizer

	if isMaster(c.user) {
		if slices.ContainsFunc(mocks, func(m evaluator.AuthorizationMockConfig) bool { return m.Decision != "allow" }) {
			return fmt.Errorf("%w: a system:masters user cannot be denied by an authorizer mock", errUnsupportedCase)
		}

		return nil
	}

	c.rbac.grants = append(c.rbac.grants, rbacRule{
		group:       c.gvr.Group,
		resource:    c.gvr.Resource,
		subresource: c.subresource,
		verb:        requestVerb(c.op),
	})

	for _, m := range mocks {
		rule := rbacRule{group: m.Group, resource: m.Resource, subresource: m.Subresource, namespace: m.Namespace, verb: m.Verb}
		if m.Decision == "allow" {
			c.rbac.grants = append(c.rbac.grants, rule)
			c.rbac.allowMocks = append(c.rbac.allowMocks, rule)

			if rule.namespace != "" {
				c.needs = append(c.needs, namespaceNeed(rule.namespace, nil))
			}
		} else {
			c.rbac.denies = append(c.rbac.denies, rule)
		}
	}

	if grant, deny, ok := c.rbac.conflict(); ok {
		return fmt.Errorf("%w: the request needs %q, which contradicts the Deny authorizer mock %q",
			errUnsupportedCase, grant, deny)
	}

	return nil
}

func requestVerb(op admissionv1.Operation) string {
	switch op {
	case admissionv1.Update:
		return "update"
	case admissionv1.Delete:
		return "delete"
	case admissionv1.Create, admissionv1.Connect:
	}

	return "create"
}

// shard is a set of cases one apiserver can run together. Its cases test one
// policy, the only one installed: the server runs every installed policy,
// while kat evaluates only the policy under test.
type shard struct {
	policy  string
	cases   []*katCase
	objects map[objectKey]need
	users   map[string]rbacNeed
}

// shardCases greedily packs cases into shards whose needs agree, so that no
// server ever has to change state it has already seeded.
func shardCases(cases []*katCase) []*shard {
	var shards []*shard

	for _, c := range cases {
		if c.isInconclusive() {
			continue
		}

		placed := false

		for _, s := range shards {
			if s.add(c) {
				placed = true

				break
			}
		}

		if !placed {
			s := &shard{policy: c.policyName, objects: map[objectKey]need{}, users: map[string]rbacNeed{}}
			s.add(c)
			shards = append(shards, s)
		}
	}

	return shards
}

// add places c into the shard if its needs agree with the shard's.
func (s *shard) add(c *katCase) bool {
	if c.policyName != s.policy {
		return false
	}

	objects := maps.Clone(s.objects)

	for _, n := range c.needs {
		existing, ok := objects[n.key]
		if !ok {
			objects[n.key] = n

			continue
		}

		merged, ok := existing.merge(n)
		if !ok {
			return false
		}

		objects[n.key] = merged
	}

	users := maps.Clone(s.users)
	if c.rbac.grants != nil || c.rbac.denies != nil {
		merged, ok := users[c.user.Name]
		if ok && !sameRules(merged.allowMocks, c.rbac.allowMocks) {
			return false
		}

		merged.user = c.user
		merged.allowMocks = c.rbac.allowMocks
		merged.grants = append(slices.Clone(merged.grants), c.rbac.grants...)
		merged.denies = append(slices.Clone(merged.denies), c.rbac.denies...)

		if _, _, conflict := merged.conflict(); conflict {
			return false
		}

		users[c.user.Name] = merged
	}

	s.objects, s.users = objects, users
	s.cases = append(s.cases, c)

	return true
}

// casesNeeding returns the shard's cases that rely on key.
func (s *shard) casesNeeding(key objectKey) []*katCase {
	var out []*katCase

	for _, c := range s.cases {
		if slices.ContainsFunc(c.needs, func(n need) bool { return n.key == key }) {
			out = append(out, c)
		}
	}

	return out
}

// sortedKeys orders keys so that namespaces are created before their contents.
func (s *shard) sortedKeys() []objectKey {
	keys := slices.Collect(maps.Keys(s.objects))
	slices.SortFunc(keys, func(a, b objectKey) int {
		aNS, bNS := a.resource == namespacesGVR(), b.resource == namespacesGVR()
		if aNS != bNS {
			if aNS {
				return -1
			}

			return 1
		}

		return strings.Compare(a.String(), b.String())
	})

	return keys
}
