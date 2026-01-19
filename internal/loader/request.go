package loader

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/yaml"

	"github.com/zemanlx/kat/internal/evaluator"
)

var (
	errAPIVersionRequired = errors.New("apiVersion is required")
	errKindRequired       = errors.New("kind is required")
	errKindMismatch       = errors.New("kind mismatch")
)

// parseTestRequestFile parses a test request file and populates the TestRequest.
// Handles *.request.yaml (simplified AdmissionRequest format), *.object.yaml (raw Kubernetes object),
// *.oldObject.yaml (object for DELETE operations), *.params.yaml (policy parameters),
// *.annotations.yaml (expected audit annotations), and *.warnings.txt (expected warnings).
func parseTestRequestFile(testReq *testRequest) error {
	data, err := os.ReadFile(testReq.FilePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	switch {
	case strings.HasSuffix(testReq.FilePath, ".request.yaml"):
		return parseRequestYAML(testReq, data)
	case strings.HasSuffix(testReq.FilePath, ".object.yaml"):
		return parseObjectYAML(testReq, data)
	case strings.HasSuffix(testReq.FilePath, ".oldObject.yaml"):
		return parseOldObjectYAML(testReq, data)
	case strings.HasSuffix(testReq.FilePath, ".params.yaml"):
		return parseParamsYAML(testReq, data)
	case strings.HasSuffix(testReq.FilePath, ".annotations.yaml"):
		return parseAnnotationsYAML(testReq, data)
	case strings.HasSuffix(testReq.FilePath, ".warnings.txt"):
		return parseWarningsFile(testReq, data)
	case strings.HasSuffix(testReq.FilePath, ".authorizer.yaml"):
		return parseAuthorizerYAML(testReq, data)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownFileType, testReq.FilePath)
	}
}

// simplifiedRequest represents the simplified requestYAML format.
type simplifiedRequest struct {
	Operation       string                     `json:"operation"`
	SubResource     string                     `json:"subResource,omitempty"`
	Name            string                     `json:"name,omitempty"`
	Namespace       string                     `json:"namespace,omitempty"`
	NamespaceObject map[string]interface{}     `json:"namespaceObject,omitempty"`
	UserInfo        *authenticationv1.UserInfo `json:"userInfo,omitempty"`
	Object          map[string]interface{}     `json:"object,omitempty"`
	OldObject       map[string]interface{}     `json:"oldObject,omitempty"`
	Options         map[string]interface{}     `json:"options,omitempty"`
}

// parseRequestYAML parses a simplified request format.
func parseRequestYAML(testReq *testRequest, data []byte) error {
	var req simplifiedRequest
	if err := yaml.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("failed to unmarshal request: %w", err)
	}

	if err := validateSimplifiedRequest(&req); err != nil {
		return err
	}

	testReq.Request = buildAdmissionRequestFromSimplified(&req, testReq)
	testReq.NamespaceName = req.Namespace

	// Parse additional objects
	if req.OldObject != nil {
		testReq.OldObject = &unstructured.Unstructured{Object: req.OldObject}
	}

	if req.NamespaceObject != nil {
		testReq.NamespaceObj = &unstructured.Unstructured{Object: req.NamespaceObject}
	}

	return nil
}

func validateSimplifiedRequest(req *simplifiedRequest) error {
	// Validate Object (lenient, might be CRD)
	if err := validateWithScheme(req.Object, "object", nil); err != nil {
		return err
	}

	// Validate OldObject (lenient, might be CRD)
	if err := validateWithScheme(req.OldObject, "oldObject", nil); err != nil {
		return err
	}

	// Validate NamespaceObject (strict, must be v1/Namespace)
	nsGVK := schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "Namespace",
	}
	if err := validateWithScheme(req.NamespaceObject, "namespaceObject", &nsGVK); err != nil {
		return err
	}

	return nil
}

func validateWithScheme(obj map[string]interface{}, field string, expectedGVK *schema.GroupVersionKind) error {
	if obj == nil {
		return nil
	}

	// Basic check for Kind and APIVersion existence
	u := &unstructured.Unstructured{Object: obj}
	if u.GetAPIVersion() == "" {
		return fmt.Errorf("%s: %w", field, errAPIVersionRequired)
	}

	if u.GetKind() == "" {
		return fmt.Errorf("%s: %w", field, errKindRequired)
	}

	return validateStructureStrict(obj, field, expectedGVK, u.GetKind())
}

func validateStructureStrict(obj map[string]interface{}, field string, expectedGVK *schema.GroupVersionKind, currentKind string) error {
	// Decode using the default Kubernetes scheme to strictly validate known types
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshal object: %w", err)
	}

	// Use a strict serializer to catch unknown fields (typos)
	serializer := k8sjson.NewSerializerWithOptions(
		k8sjson.DefaultMetaFactory,
		scheme.Scheme,
		scheme.Scheme,
		k8sjson.SerializerOptions{
			Yaml:   true,
			Strict: true,
		},
	)

	_, gvk, err := serializer.Decode(data, nil, nil)
	if err != nil {
		// If decoding failed, verify if it's because the kind is not registered
		if runtime.IsNotRegisteredError(err) {
			// If we require a specific GVK that should be in the scheme, this is an error
			if expectedGVK != nil {
				return fmt.Errorf("%s: %w: expected %s, got %q (or kind is unknown/unregistered)", field, errKindMismatch, expectedGVK.Kind, currentKind)
			}

			// For generic objects, it might be a CRD, so we allow unregistered kinds
			return nil
		}

		// Other decoding errors indicate invalid structure for the known type
		return fmt.Errorf("%s: invalid kubernetes object: %w", field, err)
	}

	// If successfully decoded, check if it matches expected GVK
	if expectedGVK != nil {
		if gvk.Group != expectedGVK.Group || gvk.Kind != expectedGVK.Kind {
			return fmt.Errorf("%s: %w: expected %s, got %q", field, errKindMismatch, expectedGVK.Kind, gvk.Kind)
		}
	}

	return nil
}

func buildAdmissionRequestFromSimplified(req *simplifiedRequest, testReq *testRequest) *admissionv1.AdmissionRequest {
	admReq := &admissionv1.AdmissionRequest{
		UID:         types.UID("test-" + testReq.Name),
		Operation:   admissionv1.Operation(req.Operation),
		Name:        req.Name,
		Namespace:   req.Namespace,
		SubResource: req.SubResource,
	}

	if req.UserInfo != nil {
		admReq.UserInfo = *req.UserInfo
		testReq.UserInfo = req.UserInfo
	}

	if req.Object != nil {
		obj := &unstructured.Unstructured{Object: req.Object}
		testReq.Object = obj

		gvk := obj.GroupVersionKind()
		admReq.Resource = metav1.GroupVersionResource{
			Group:    gvk.Group,
			Version:  gvk.Version,
			Resource: strings.ToLower(gvk.Kind) + "s",
		}
		admReq.Kind = metav1.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind,
		}
	}

	if req.Options != nil {
		optionsBytes, _ := yaml.Marshal(req.Options)
		admReq.Options = runtime.RawExtension{Raw: optionsBytes}
	}

	return admReq
}

// parseObjectYAML parses a raw Kubernetes object and creates an AdmissionRequest for it.
func parseObjectYAML(testReq *testRequest, data []byte) error {
	var obj map[string]interface{}
	if err := yaml.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("failed to unmarshal object: %w", err)
	}

	if err := validateWithScheme(obj, "object", nil); err != nil {
		return err
	}

	unstruct := &unstructured.Unstructured{Object: obj}
	testReq.Object = unstruct
	testReq.Request = buildCreateRequestFromObject(testReq.Name, unstruct)
	testReq.NamespaceName = unstruct.GetNamespace()

	if err := loadAuxiliaryFiles(testReq); err != nil {
		return err
	}

	return nil
}

func buildCreateRequestFromObject(testName string, obj *unstructured.Unstructured) *admissionv1.AdmissionRequest {
	gvk := obj.GroupVersionKind()

	return &admissionv1.AdmissionRequest{
		UID:       types.UID("test-" + testName),
		Operation: admissionv1.Create,
		Kind: metav1.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind,
		},
		Resource: metav1.GroupVersionResource{
			Group:    gvk.Group,
			Version:  gvk.Version,
			Resource: strings.ToLower(gvk.Kind) + "s",
		},
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}
}

func loadAuxiliaryFiles(testReq *testRequest) error {
	// Look for corresponding .gold.yaml file (expected mutated object)
	if err := loadGoldFile(testReq); err != nil {
		return err
	}

	// Look for corresponding .params.yaml file
	if err := loadParamsFile(testReq); err != nil {
		return err
	}

	// Look for corresponding .message.txt file (expected error message)
	if err := loadMessageFile(testReq); err != nil {
		return err
	}

	// Look for corresponding .authorizer.yaml file
	if err := loadAuthorizerFile(testReq); err != nil {
		return err
	}

	return nil
}

func loadGoldFile(testReq *testRequest) error {
	goldPath := strings.Replace(testReq.FilePath, ".object.yaml", ".gold.yaml", 1)
	if _, err := os.Stat(goldPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return fmt.Errorf("stat gold file: %w", err)
	}

	goldData, err := os.ReadFile(goldPath)
	if err != nil {
		return fmt.Errorf("failed to read gold file: %w", err)
	}

	var goldObj map[string]interface{}
	if err := yaml.Unmarshal(goldData, &goldObj); err != nil {
		return fmt.Errorf("failed to unmarshal gold object: %w", err)
	}

	testReq.ExpectedObject = &unstructured.Unstructured{Object: goldObj}
	testReq.ExpectMutated = true

	return nil
}

func loadParamsFile(testReq *testRequest) error {
	paramsPath := strings.Replace(testReq.FilePath, ".object.yaml", ".params.yaml", 1)
	paramsPath = strings.Replace(paramsPath, ".request.yaml", ".params.yaml", 1)

	if _, err := os.Stat(paramsPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return fmt.Errorf("stat params file: %w", err)
	}

	paramsData, err := os.ReadFile(paramsPath)
	if err != nil {
		return fmt.Errorf("failed to read params file: %w", err)
	}

	var paramsObj map[string]interface{}
	if err := yaml.Unmarshal(paramsData, &paramsObj); err != nil {
		return fmt.Errorf("failed to unmarshal params object: %w", err)
	}

	testReq.Params = &unstructured.Unstructured{Object: paramsObj}

	return nil
}

func loadMessageFile(testReq *testRequest) error {
	messagePath := strings.Replace(testReq.FilePath, ".object.yaml", ".message.txt", 1)
	messagePath = strings.Replace(messagePath, ".request.yaml", ".message.txt", 1)

	if _, err := os.Stat(messagePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return fmt.Errorf("stat message file: %w", err)
	}

	messageData, err := os.ReadFile(messagePath)
	if err != nil {
		return fmt.Errorf("failed to read message file: %w", err)
	}

	testReq.ExpectMessage = strings.TrimSpace(string(messageData))

	return nil
}

func loadAuthorizerFile(testReq *testRequest) error {
	authPath := strings.Replace(testReq.FilePath, ".object.yaml", ".authorizer.yaml", 1)
	authPath = strings.Replace(authPath, ".request.yaml", ".authorizer.yaml", 1)

	if _, err := os.Stat(authPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return fmt.Errorf("stat authorizer file: %w", err)
	}

	authData, err := os.ReadFile(authPath)
	if err != nil {
		return fmt.Errorf("failed to read authorizer file: %w", err)
	}

	return parseAuthorizerYAML(testReq, authData)
}

// parseOldObjectYAML parses a raw Kubernetes object and creates an AdmissionRequest for DELETE operation.
// This is used for testing deletion policies where only oldObject is relevant.
func parseOldObjectYAML(testReq *testRequest, data []byte) error {
	var obj map[string]interface{}
	if err := yaml.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("failed to unmarshal oldObject: %w", err)
	}

	if err := validateWithScheme(obj, "oldObject", nil); err != nil {
		return err
	}

	unstruct := &unstructured.Unstructured{Object: obj}
	testReq.OldObject = unstruct

	// Build AdmissionRequest for DELETE operation
	gvk := unstruct.GroupVersionKind()
	admReq := &admissionv1.AdmissionRequest{
		UID:       types.UID("test-" + testReq.Name),
		Operation: admissionv1.Delete,
		Kind: metav1.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind,
		},
		Resource: metav1.GroupVersionResource{
			Group:    gvk.Group,
			Version:  gvk.Version,
			Resource: strings.ToLower(gvk.Kind) + "s",
		},
		Name:      unstruct.GetName(),
		Namespace: unstruct.GetNamespace(),
	}

	testReq.Request = admReq
	testReq.NamespaceName = unstruct.GetNamespace()

	// Look for corresponding .params.yaml file
	paramsPath := strings.Replace(testReq.FilePath, ".oldObject.yaml", ".params.yaml", 1)
	if _, err := os.Stat(paramsPath); err == nil {
		paramsData, err := os.ReadFile(paramsPath)
		if err != nil {
			return fmt.Errorf("failed to read params file: %w", err)
		}

		var paramsObj map[string]interface{}
		if err := yaml.Unmarshal(paramsData, &paramsObj); err != nil {
			return fmt.Errorf("failed to unmarshal params object: %w", err)
		}

		testReq.Params = &unstructured.Unstructured{Object: paramsObj}
	}

	// Look for corresponding .message.txt file (expected error message)
	messagePath := strings.Replace(testReq.FilePath, ".oldObject.yaml", ".message.txt", 1)
	if _, err := os.Stat(messagePath); err == nil {
		messageData, err := os.ReadFile(messagePath)
		if err != nil {
			return fmt.Errorf("failed to read message file: %w", err)
		}

		testReq.ExpectMessage = strings.TrimSpace(string(messageData))
	}

	return nil
}

// loadGoldFile loads the expected object from a .gold.yaml file.
// parseParamsYAML parses a policy parameters file (ConfigMap or custom resource).
func parseParamsYAML(testReq *testRequest, data []byte) error {
	var obj map[string]interface{}
	if err := yaml.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("failed to unmarshal params: %w", err)
	}

	testReq.Params = &unstructured.Unstructured{Object: obj}

	return nil
}

// parseAnnotationsYAML parses expected audit annotations file.
func parseAnnotationsYAML(testReq *testRequest, data []byte) error {
	var annotations map[string]string
	if err := yaml.Unmarshal(data, &annotations); err != nil {
		return fmt.Errorf("failed to unmarshal annotations: %w", err)
	}

	testReq.ExpectAuditAnnotations = annotations

	return nil
}

// parseWarningsFile parses expected warnings from a text file.
// Each line is treated as a separate warning message.
func parseWarningsFile(testReq *testRequest, data []byte) error {
	content := strings.TrimSpace(string(data))
	if content == "" {
		return nil
	}

	// Split by newlines and filter empty lines
	lines := strings.Split(content, "\n")

	var warnings []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			warnings = append(warnings, line)
		}
	}

	testReq.ExpectWarnings = warnings

	return nil
}

// parseAuthorizerYAML parses expected authorizer mock configuration.
func parseAuthorizerYAML(testReq *testRequest, data []byte) error {
	var mocks []evaluator.AuthorizationMockConfig
	if err := yaml.Unmarshal(data, &mocks); err != nil {
		return fmt.Errorf("failed to unmarshal authorizer mocks: %w", err)
	}

	testReq.Authorizer = mocks

	return nil
}

// InferOperation determines the Kubernetes admission operation based on which YAML files are present.
// If requestOpStr is non-empty, it's used directly (for explicit CONNECT operations).
// Otherwise, operation is inferred from the presence of object/oldObject files:
//   - object only -> CREATE
//   - oldObject only -> DELETE
//   - both object and oldObject -> UPDATE
func InferOperation(hasObject, hasOldObject bool, requestOpStr string) (string, error) {
	// Explicit operation from request.yaml (e.g., CONNECT)
	if requestOpStr != "" {
		return requestOpStr, nil
	}

	// Infer from file presence
	switch {
	case hasObject && !hasOldObject:
		return string(admissionv1.Create), nil
	case !hasObject && hasOldObject:
		return string(admissionv1.Delete), nil
	case hasObject && hasOldObject:
		return string(admissionv1.Update), nil
	default:
		return "", fmt.Errorf("%w: no object/oldObject files and no explicit operation", ErrCannotInferOperation)
	}
}
