package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
)

// probeNamespace holds the harness's own readiness probe objects.
const probeNamespace = "kat-system"

var (
	errNamespaceLabels = errors.New("pre-existing namespace has different labels or annotations")
	errDenyGranted     = errors.New("the server allows a check the case mocks as denied")
)

// resource returns a dynamic client for key's resource and namespace.
func (s *server) resource(key objectKey) dynamic.ResourceInterface {
	if key.namespace == "" {
		return s.dynamic.Resource(key.resource)
	}

	return s.dynamic.Resource(key.resource).Namespace(key.namespace)
}

// seed creates everything the shard's cases need to be stored, before any
// policy exists, so that the policies under test never see the seeding
// requests. Failures are attributed to the cases that needed the object.
func (h *shardRun) seed(ctx context.Context) error {
	if err := h.srv.ensure(ctx, namespaceNeed(probeNamespace, nil)); err != nil {
		return fmt.Errorf("create %s namespace: %w", probeNamespace, err)
	}

	for _, key := range h.shard.sortedKeys() {
		n := h.shard.objects[key]
		if !n.present {
			continue
		}

		if err := h.srv.ensure(ctx, n); err != nil {
			for _, c := range h.shard.casesNeeding(key) {
				c.inconclusivef("seed %s: %v", key, err)
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(h.shard.users)) {
		if err := h.srv.grant(ctx, h.shard.users[name]); err != nil {
			for _, c := range h.shard.cases {
				if c.user.Name == name {
					c.inconclusivef("RBAC for %q: %v", name, err)
				}
			}
		}
	}

	return nil
}

// ensure stores the needed object. Pre-existing namespaces such as default
// are reused as long as the case does not need different labels on them.
func (s *server) ensure(ctx context.Context, n need) error {
	if n.key.resource == namespacesGVR() {
		existing, err := s.client.CoreV1().Namespaces().Get(ctx, n.key.name, metav1.GetOptions{})

		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			return fmt.Errorf("get namespace: %w", err)
		case n.loose:
			return nil
		default:
			if !isSubset(n.object.GetLabels(), existing.Labels) || !isSubset(n.object.GetAnnotations(), existing.Annotations) {
				return fmt.Errorf("%w: %s", errNamespaceLabels, n.key.name)
			}

			return nil
		}
	}

	if _, err := s.resource(n.key).Create(ctx, n.object.DeepCopy(), metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create: %w", err)
	}

	return nil
}

func isSubset(want, have map[string]string) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}

	return true
}

// grant binds the user to roles holding exactly the needed grants, waits
// until RBAC allows them, and checks that every mocked Deny stays denied.
func (s *server) grant(ctx context.Context, need rbacNeed) error {
	sum := sha256.Sum256([]byte(need.user.Name))
	name := "kat-conformance-" + hex.EncodeToString(sum[:8])
	subject := rbacv1.Subject{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: need.user.Name}

	byNamespace := map[string][]rbacv1.PolicyRule{}
	for _, g := range need.grants {
		byNamespace[g.namespace] = append(byNamespace[g.namespace], policyRule(g))
	}

	for _, namespace := range slices.Sorted(maps.Keys(byNamespace)) {
		if err := s.bindRules(ctx, name, namespace, subject, byNamespace[namespace]); err != nil {
			return err
		}
	}

	for _, g := range need.grants {
		if err := s.waitAccess(ctx, need, g); err != nil {
			return err
		}
	}

	for _, d := range need.denies {
		allowed, err := s.access(ctx, need, d)
		if err != nil {
			return err
		}

		if allowed {
			return fmt.Errorf("%w: %s", errDenyGranted, d)
		}
	}

	return nil
}

func policyRule(r rbacRule) rbacv1.PolicyRule {
	resource := r.resource
	if r.subresource != "" {
		resource += "/" + r.subresource
	}

	return rbacv1.PolicyRule{APIGroups: []string{r.group}, Resources: []string{resource}, Verbs: []string{r.verb}}
}

func (s *server) bindRules(ctx context.Context, name, namespace string, subject rbacv1.Subject, rules []rbacv1.PolicyRule) error {
	rbac := s.client.RbacV1()
	meta := metav1.ObjectMeta{Name: name, Namespace: namespace}

	if namespace == "" {
		if _, err := rbac.ClusterRoles().Create(ctx, &rbacv1.ClusterRole{ObjectMeta: meta, Rules: rules}, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create ClusterRole: %w", err)
		}

		binding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: meta,
			Subjects:   []rbacv1.Subject{subject},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name},
		}
		if _, err := rbac.ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create ClusterRoleBinding: %w", err)
		}

		return nil
	}

	if _, err := rbac.Roles(namespace).Create(ctx, &rbacv1.Role{ObjectMeta: meta, Rules: rules}, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create Role: %w", err)
	}

	binding := &rbacv1.RoleBinding{
		ObjectMeta: meta,
		Subjects:   []rbacv1.Subject{subject},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
	}
	if _, err := rbac.RoleBindings(namespace).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create RoleBinding: %w", err)
	}

	return nil
}

// waitAccess waits for RBAC's informers to pick up a new grant.
func (s *server) waitAccess(ctx context.Context, need rbacNeed, rule rbacRule) error {
	const (
		interval = 50 * time.Millisecond
		timeout  = 30 * time.Second
	)

	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		return s.access(ctx, need, rule)
	})
	if err != nil {
		return fmt.Errorf("wait for RBAC to allow %s: %w", rule, err)
	}

	return nil
}

func (s *server) access(ctx context.Context, need rbacNeed, rule rbacRule) (bool, error) {
	extra := make(map[string]authorizationv1.ExtraValue, len(need.user.Extra))
	for k, v := range need.user.Extra {
		extra[k] = v
	}

	review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User:   need.user.Name,
		UID:    need.user.UID,
		Groups: need.user.Groups,
		Extra:  extra,
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace:   rule.namespace,
			Verb:        rule.verb,
			Group:       rule.group,
			Resource:    rule.resource,
			Subresource: rule.subresource,
		},
	}}

	got, err := s.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("SubjectAccessReview: %w", err)
	}

	return got.Status.Allowed, nil
}

// get reads a stored object as the admin.
func (s *server) get(ctx context.Context, key objectKey) (*unstructured.Unstructured, error) {
	u, err := s.resource(key).Get(ctx, key.name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}

	return u, nil
}
