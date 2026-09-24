package evaluator

import (
	"fmt"
	"slices"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	predicaterules "k8s.io/apiserver/pkg/admission/plugin/webhook/predicates/rules"
)

// policyApplies reports whether the request is selected by both the policy's
// matchConstraints and the binding's matchResources. A nil MatchResources
// selects everything.
func policyApplies(
	constraints *admissionregv1.MatchResources,
	bindingResources *admissionregv1.MatchResources,
	request *admissionv1.AdmissionRequest,
	object *unstructured.Unstructured,
	oldObject *unstructured.Unstructured,
	namespaceObj *unstructured.Unstructured,
) (bool, error) {
	attr := newAttributes(request, object, oldObject, namespaceObj)

	for _, mr := range []*admissionregv1.MatchResources{constraints, bindingResources} {
		matched, err := matchesResources(mr, attr, namespaceObj)
		if err != nil || !matched {
			return false, err
		}
	}

	return true, nil
}

// matchesResources follows the API server's order: namespaceSelector,
// objectSelector, excludeResourceRules, then resourceRules (empty matches all).
//
// matchPolicy Equivalent is evaluated as Exact: kat has no API discovery to
// map a request onto equivalent group/versions.
func matchesResources(
	mr *admissionregv1.MatchResources,
	attr admission.Attributes,
	namespaceObj *unstructured.Unstructured,
) (bool, error) {
	if mr == nil {
		return true, nil
	}

	if matched, err := matchesNamespaceSelector(mr.NamespaceSelector, attr, namespaceObj); err != nil || !matched {
		return false, err
	}

	if matched, err := matchesObjectSelector(mr.ObjectSelector, attr); err != nil || !matched {
		return false, err
	}

	if matchesAnyRule(mr.ExcludeResourceRules, attr) {
		return false, nil
	}

	return len(mr.ResourceRules) == 0 || matchesAnyRule(mr.ResourceRules, attr), nil
}

// matchesAnyRule reports whether a single rule matches the request on
// operation, apiGroup, apiVersion, resource/subresource, scope and resourceNames.
func matchesAnyRule(rules []admissionregv1.NamedRuleWithOperations, attr admission.Attributes) bool {
	for _, rule := range rules {
		m := predicaterules.Matcher{Rule: rule.RuleWithOperations, Attr: attr}
		if !m.Matches() {
			continue
		}

		if len(rule.ResourceNames) == 0 || slices.Contains(rule.ResourceNames, attr.GetName()) {
			return true
		}
	}

	return false
}

// matchesNamespaceSelector matches the selector against the namespace labels.
// For CREATE/UPDATE of a Namespace itself the object's own labels are used, as
// in the API server. Unlike the API server, a test without a namespaceObject
// matches, since there is no cluster to look the namespace up in.
func matchesNamespaceSelector(
	labelSelector *metav1.LabelSelector,
	attr admission.Attributes,
	namespaceObj *unstructured.Unstructured,
) (bool, error) {
	if labelSelector == nil {
		return true, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(labelSelector)
	if err != nil {
		return false, fmt.Errorf("parse namespace selector: %w", err)
	}

	if selector.Empty() {
		return true, nil
	}

	op := attr.GetOperation()
	if attr.GetResource().Resource == "namespaces" && attr.GetSubresource() == "" &&
		(op == admission.Create || op == admission.Update) {
		return hasMatchingLabels(attr.GetObject(), selector), nil
	}

	if namespaceObj == nil {
		return true, nil
	}

	return selector.Matches(labels.Set(namespaceObj.GetLabels())), nil
}

// matchesObjectSelector matches if either the object or the old object carries
// matching labels; a missing object never matches a non-empty selector.
func matchesObjectSelector(labelSelector *metav1.LabelSelector, attr admission.Attributes) (bool, error) {
	if labelSelector == nil {
		return true, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(labelSelector)
	if err != nil {
		return false, fmt.Errorf("parse object selector: %w", err)
	}

	if selector.Empty() {
		return true, nil
	}

	return hasMatchingLabels(attr.GetObject(), selector) || hasMatchingLabels(attr.GetOldObject(), selector), nil
}

func hasMatchingLabels(obj runtime.Object, selector labels.Selector) bool {
	if obj == nil {
		return false
	}

	accessor, err := meta.Accessor(obj)
	if err != nil {
		return false
	}

	return selector.Matches(labels.Set(accessor.GetLabels()))
}

// newAttributes adapts a test's admission request to admission.Attributes. When
// the request carries no namespace, the namespaceObject's name stands in so that
// rule scope sees the request as namespaced.
func newAttributes(
	req *admissionv1.AdmissionRequest,
	object *unstructured.Unstructured,
	oldObject *unstructured.Unstructured,
	namespaceObj *unstructured.Unstructured,
) admission.Attributes {
	if req == nil {
		req = &admissionv1.AdmissionRequest{}
	}

	namespace := req.Namespace
	if namespace == "" && namespaceObj != nil {
		namespace = namespaceObj.GetName()
	}

	return admission.NewAttributesRecord(
		asRuntimeObject(object),
		asRuntimeObject(oldObject),
		schema.GroupVersionKind{Group: req.Kind.Group, Version: req.Kind.Version, Kind: req.Kind.Kind},
		namespace,
		req.Name,
		schema.GroupVersionResource{Group: req.Resource.Group, Version: req.Resource.Version, Resource: req.Resource.Resource},
		req.SubResource,
		admission.Operation(req.Operation),
		nil,
		false,
		nil,
	)
}

// asRuntimeObject keeps a nil *Unstructured from becoming a non-nil interface.
func asRuntimeObject(u *unstructured.Unstructured) runtime.Object {
	if u == nil {
		return nil
	}

	return u
}
