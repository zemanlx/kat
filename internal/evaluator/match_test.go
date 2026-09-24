package evaluator

import (
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func testRule(ops []admissionregv1.OperationType, group, version string, resources ...string) admissionregv1.NamedRuleWithOperations {
	return admissionregv1.NamedRuleWithOperations{
		Operations:  ops,
		APIGroups:   []string{group},
		APIVersions: []string{version},
		Resources:   resources,
	}
}

func testRequest(op admissionv1.Operation, group, kind, resource string) *admissionv1.AdmissionRequest {
	return &admissionv1.AdmissionRequest{
		Operation: op,
		Name:      "web",
		Namespace: "default",
		Kind:      metav1.GroupVersionKind{Group: group, Version: "v1", Kind: kind},
		Resource:  metav1.GroupVersionResource{Group: group, Version: "v1", Resource: resource},
	}
}

func testLabeled(kind, name string, lbls map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "labels": lbls},
	}}
}

//nolint:funlen // Table of matching cases
func TestPolicyApplies(t *testing.T) {
	t.Parallel()

	var (
		create  = []admissionregv1.OperationType{admissionregv1.Create}
		update  = []admissionregv1.OperationType{admissionregv1.Update}
		connect = []admissionregv1.OperationType{admissionregv1.Connect}
		all     = []admissionregv1.OperationType{admissionregv1.OperationAll}
		cu      = []admissionregv1.OperationType{admissionregv1.Create, admissionregv1.Update}
	)

	podCreate := testRequest(admissionv1.Create, "", "Pod", "pods")
	podUpdate := testRequest(admissionv1.Update, "", "Pod", "pods")
	deployUpdate := testRequest(admissionv1.Update, "apps", "Deployment", "deployments")

	podExec := testRequest(admissionv1.Connect, "", "PodExecOptions", "pods")
	podExec.SubResource = "exec"

	clusterRoleCreate := testRequest(admissionv1.Create, "rbac.authorization.k8s.io", "ClusterRole", "clusterroles")
	clusterRoleCreate.Namespace = ""

	nsCreate := testRequest(admissionv1.Create, "", "Namespace", "namespaces")
	nsCreate.Namespace = ""

	rules := func(r ...admissionregv1.NamedRuleWithOperations) *admissionregv1.MatchResources {
		return &admissionregv1.MatchResources{ResourceRules: r}
	}
	namespaced := admissionregv1.NamespacedScope
	clusterScope := admissionregv1.ClusterScope

	withScope := func(r admissionregv1.NamedRuleWithOperations, s admissionregv1.ScopeType) admissionregv1.NamedRuleWithOperations {
		r.Scope = &s

		return r
	}
	withNames := func(r admissionregv1.NamedRuleWithOperations, names ...string) admissionregv1.NamedRuleWithOperations {
		r.ResourceNames = names

		return r
	}

	prodSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}}
	prodNamespace := testLabeled("Namespace", "default", map[string]any{"env": "prod"})
	devNamespace := testLabeled("Namespace", "default", map[string]any{"env": "dev"})
	prodPod := testLabeled("Pod", "web", map[string]any{"env": "prod"})
	devPod := testLabeled("Pod", "web", map[string]any{"env": "dev"})

	tests := []struct {
		name         string
		constraints  *admissionregv1.MatchResources
		binding      *admissionregv1.MatchResources
		req          *admissionv1.AdmissionRequest
		object       *unstructured.Unstructured
		oldObject    *unstructured.Unstructured
		namespaceObj *unstructured.Unstructured
		want         bool
		wantErr      bool
	}{
		{name: "nil constraints match", req: podCreate, want: true},
		{name: "empty rules match", constraints: &admissionregv1.MatchResources{}, req: podCreate, want: true},

		{name: "operation listed", constraints: rules(testRule(create, "", "v1", "pods")), req: podCreate, want: true},
		{name: "operation not listed", constraints: rules(testRule(create, "", "v1", "pods")), req: podUpdate, want: false},
		{name: "operation wildcard", constraints: rules(testRule(all, "", "v1", "pods")), req: podUpdate, want: true},
		{
			name:        "operations are per rule, not unioned across rules",
			constraints: rules(testRule(create, "", "v1", "pods"), testRule(update, "apps", "v1", "deployments")),
			req:         podUpdate,
			want:        false,
		},
		{
			name:        "second rule matches its own resource",
			constraints: rules(testRule(create, "", "v1", "pods"), testRule(update, "apps", "v1", "deployments")),
			req:         deployUpdate,
			want:        true,
		},

		{name: "apiGroup mismatch", constraints: rules(testRule(cu, "apps", "v1", "pods")), req: podCreate, want: false},
		{name: "apiVersion mismatch", constraints: rules(testRule(cu, "", "v2", "pods")), req: podCreate, want: false},
		{name: "resource mismatch", constraints: rules(testRule(cu, "", "v1", "services")), req: podCreate, want: false},
		{name: "wildcard group/version/resource", constraints: rules(testRule(cu, "*", "*", "*")), req: podCreate, want: true},

		{name: "subresource rule matches", constraints: rules(testRule(connect, "", "v1", "pods/exec")), req: podExec, want: true},
		{name: "subresource wildcard", constraints: rules(testRule(connect, "", "v1", "pods/*")), req: podExec, want: true},
		{name: "plain resource does not match subresource", constraints: rules(testRule(connect, "", "v1", "pods")), req: podExec, want: false},
		{name: "subresource rule does not match main resource", constraints: rules(testRule(cu, "", "v1", "pods/status")), req: podCreate, want: false},

		{name: "resourceNames listed", constraints: rules(withNames(testRule(cu, "", "v1", "pods"), "web")), req: podCreate, want: true},
		{name: "resourceNames not listed", constraints: rules(withNames(testRule(cu, "", "v1", "pods"), "other")), req: podCreate, want: false},

		{name: "namespaced scope matches namespaced request", constraints: rules(withScope(testRule(cu, "", "v1", "pods"), namespaced)), req: podCreate, want: true},
		{
			name:        "namespaced scope skips cluster-scoped request",
			constraints: rules(withScope(testRule(cu, "*", "*", "*"), namespaced)),
			req:         clusterRoleCreate,
			want:        false,
		},
		{
			name:        "cluster scope includes namespaces",
			constraints: rules(withScope(testRule(cu, "", "v1", "namespaces"), clusterScope)),
			req:         nsCreate,
			want:        true,
		},

		{
			name: "excludeResourceRules win over resourceRules",
			constraints: &admissionregv1.MatchResources{
				ResourceRules:        []admissionregv1.NamedRuleWithOperations{testRule(cu, "", "v1", "pods")},
				ExcludeResourceRules: []admissionregv1.NamedRuleWithOperations{withNames(testRule(cu, "", "v1", "pods"), "web")},
			},
			req:  podCreate,
			want: false,
		},
		{
			name: "excludeResourceRules for another name do not exclude",
			constraints: &admissionregv1.MatchResources{
				ResourceRules:        []admissionregv1.NamedRuleWithOperations{testRule(cu, "", "v1", "pods")},
				ExcludeResourceRules: []admissionregv1.NamedRuleWithOperations{withNames(testRule(cu, "", "v1", "pods"), "other")},
			},
			req:  podCreate,
			want: true,
		},

		{name: "objectSelector matches object", constraints: &admissionregv1.MatchResources{ObjectSelector: prodSelector}, req: podCreate, object: prodPod, want: true},
		{name: "objectSelector skips object", constraints: &admissionregv1.MatchResources{ObjectSelector: prodSelector}, req: podCreate, object: devPod, want: false},
		{
			name:        "objectSelector matches oldObject on UPDATE",
			constraints: &admissionregv1.MatchResources{ObjectSelector: prodSelector},
			req:         podUpdate,
			object:      devPod,
			oldObject:   prodPod,
			want:        true,
		},
		{name: "objectSelector without objects never matches", constraints: &admissionregv1.MatchResources{ObjectSelector: prodSelector}, req: podExec, want: false},

		{
			name:         "namespaceSelector matches namespaceObject",
			constraints:  &admissionregv1.MatchResources{NamespaceSelector: prodSelector},
			req:          podCreate,
			namespaceObj: prodNamespace,
			want:         true,
		},
		{
			name:         "namespaceSelector skips namespaceObject",
			constraints:  &admissionregv1.MatchResources{NamespaceSelector: prodSelector},
			req:          podCreate,
			namespaceObj: devNamespace,
			want:         false,
		},
		{name: "namespaceSelector without namespaceObject matches", constraints: &admissionregv1.MatchResources{NamespaceSelector: prodSelector}, req: podCreate, want: true},
		{
			name:        "namespaceSelector on Namespace CREATE uses its own labels",
			constraints: &admissionregv1.MatchResources{NamespaceSelector: prodSelector},
			req:         nsCreate,
			object:      devNamespace,
			want:        false,
		},

		{
			name:        "binding matchResources narrow the policy",
			constraints: rules(testRule(cu, "", "v1", "pods")),
			binding:     rules(testRule(create, "", "v1", "pods")),
			req:         podUpdate,
			want:        false,
		},
		{
			name:         "binding namespaceSelector applies",
			constraints:  rules(testRule(cu, "", "v1", "pods")),
			binding:      &admissionregv1.MatchResources{NamespaceSelector: prodSelector},
			req:          podCreate,
			namespaceObj: devNamespace,
			want:         false,
		},

		{
			name: "invalid selector errors",
			constraints: &admissionregv1.MatchResources{ObjectSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "env", Operator: "Bogus"}},
			}},
			req:     podCreate,
			object:  prodPod,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := policyApplies(tc.constraints, tc.binding, tc.req, tc.object, tc.oldObject, tc.namespaceObj)
			if (err != nil) != tc.wantErr {
				t.Fatalf("policyApplies() error = %v, wantErr %v", err, tc.wantErr)
			}

			if got != tc.want {
				t.Errorf("policyApplies() = %v, want %v", got, tc.want)
			}
		})
	}
}
