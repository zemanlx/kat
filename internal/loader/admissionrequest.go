package loader

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
	admission "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// BuildAdmissionRequest constructs a k8s.io/api/admission/v1.AdmissionRequest from loaded YAML data.
// It requires operation, request bytes, and object/oldObject bytes as appropriate for the operation.
//
//nolint:cyclop // Operation-specific branching and metadata inference
func BuildAdmissionRequest(operation string, objectBytes, oldObjBytes, requestYAML []byte) (*admission.AdmissionRequest, error) {
	// Parse complete request YAML into AdmissionRequest
	req, err := parseAdmissionRequestYAML(requestYAML)
	if err != nil {
		return nil, err
	}

	// Set operation if not already set
	if req.Operation == "" {
		req.Operation = admission.Operation(operation)
	}

	// Validate required data per operation
	switch req.Operation {
	case admission.Create:
		if len(objectBytes) == 0 {
			return nil, ErrCreateRequiresObject
		}
	case admission.Delete:
		if len(oldObjBytes) == 0 {
			return nil, ErrDeleteRequiresOldObject
		}
	case admission.Update:
		if len(objectBytes) == 0 {
			return nil, ErrUpdateRequiresObject
		}

		if len(oldObjBytes) == 0 {
			return nil, ErrUpdateRequiresOldObject
		}
	case admission.Connect:
		// CONNECT operations only need request metadata
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownOperation, req.Operation)
	}

	// Set object and oldObject from bytes
	if len(objectBytes) > 0 {
		req.Object = runtime.RawExtension{Raw: objectBytes}
		// Infer name, namespace, kind, apiVersion, resource from object if not set
		if err := inferFromObject(req, objectBytes); err != nil {
			return nil, fmt.Errorf("infer from object: %w", err)
		}
	}

	if len(oldObjBytes) > 0 {
		req.OldObject = runtime.RawExtension{Raw: oldObjBytes}
		// For UPDATE/DELETE, always infer from oldObject; inferFromObject only fills missing metadata
		if err := inferFromObject(req, oldObjBytes); err != nil {
			return nil, fmt.Errorf("infer from oldObject: %w", err)
		}
	}

	// Use default UID if not set
	if req.UID == "" {
		req.UID = "test-uid"
	}

	return req, nil
}

// inferFromObject extracts metadata from a Kubernetes object to populate AdmissionRequest fields.
//
//nolint:cyclop // Extracts many optional fields from arbitrary Kubernetes objects
func inferFromObject(req *admission.AdmissionRequest, objBytes []byte) error {
	// Parse YAML to map
	var data map[string]any
	if err := yaml.Unmarshal(objBytes, &data); err != nil {
		return fmt.Errorf("unmarshal object: %w", err)
	}

	// Use unstructured accessor directly with the parsed data
	unst := &unstructured.Unstructured{Object: data}

	// Extract GroupVersionKind from the unstructured object
	gvk := unst.GetObjectKind().GroupVersionKind()
	if req.Kind.Kind == "" && gvk.Kind != "" {
		req.Kind.Kind = gvk.Kind
		req.Kind.Group = gvk.Group
		req.Kind.Version = gvk.Version
	}

	// Infer resource name from kind
	if req.Resource.Resource == "" && req.Kind.Kind != "" {
		req.Resource.Resource = kindToResource(req.Kind.Kind)
	}
	// Always set Group and Version from Kind if we have it and they're empty
	if req.Resource.Group == "" {
		req.Resource.Group = req.Kind.Group
	}

	if req.Resource.Version == "" {
		req.Resource.Version = req.Kind.Version
	}

	// Set RequestKind and RequestResource
	if req.RequestKind == nil && req.Kind.Kind != "" {
		req.RequestKind = &metav1.GroupVersionKind{
			Group:   req.Kind.Group,
			Version: req.Kind.Version,
			Kind:    req.Kind.Kind,
		}
	}

	if req.RequestResource == nil && req.Resource.Resource != "" {
		req.RequestResource = &metav1.GroupVersionResource{
			Group:    req.Resource.Group,
			Version:  req.Resource.Version,
			Resource: req.Resource.Resource,
		}
	}

	// Extract metadata using Kubernetes accessor methods
	if req.Name == "" {
		req.Name = unst.GetName()
	}

	if req.Namespace == "" {
		req.Namespace = unst.GetNamespace()
	}

	if req.UID == "" {
		req.UID = unst.GetUID()
	}

	return nil
}

// kindToResource converts a Kubernetes kind to its resource name using Kubernetes' pluralization rules.
func kindToResource(kind string) string {
	// Use Kubernetes' built-in conversion which handles special cases properly
	gvr, _ := meta.UnsafeGuessKindToResource(schema.GroupVersionKind{Kind: kind})

	return gvr.Resource
}

// parseAdmissionRequestYAML parses request YAML into an AdmissionRequest via JSON unmarshaling.
// This ensures compatibility with Kubernetes struct tags and proper field mapping.
func parseAdmissionRequestYAML(yamlBytes []byte) (*admission.AdmissionRequest, error) {
	req := &admission.AdmissionRequest{}

	if len(yamlBytes) == 0 {
		// Empty YAML returns zero-value AdmissionRequest
		return req, nil
	}

	// Parse YAML to map
	var data map[string]any
	if err := yaml.Unmarshal(yamlBytes, &data); err != nil {
		return nil, fmt.Errorf("parse request YAML: %w", err)
	}

	// Convert map to JSON bytes
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("parse request YAML: %w", err)
	}

	// Unmarshal JSON to AdmissionRequest
	if err := json.Unmarshal(jsonBytes, req); err != nil {
		return nil, fmt.Errorf("parse request YAML: %w", err)
	}

	return req, nil
}
