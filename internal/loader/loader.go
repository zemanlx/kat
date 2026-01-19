package loader

import (
	"fmt"
	"os"

	admission "k8s.io/api/admission/v1"
)

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
		return string(admission.Create), nil
	case !hasObject && hasOldObject:
		return string(admission.Delete), nil
	case hasObject && hasOldObject:
		return string(admission.Update), nil
	default:
		return "", fmt.Errorf("%w: no object/oldObject files and no explicit operation", ErrCannotInferOperation)
	}
}

// LoadYAML reads and returns raw YAML file bytes without parsing.
func LoadYAML(filePath string) ([]byte, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filePath, err)
	}

	return data, nil
}
