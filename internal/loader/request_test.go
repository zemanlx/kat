package loader

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

//nolint:funlen // Table-driven test with many cases
func TestValidateWithScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		obj         map[string]interface{}
		field       string
		expectedGVK *schema.GroupVersionKind
		wantErr     bool
	}{
		{
			name: "valid pod",
			obj: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata": map[string]interface{}{
					"name": "test-pod",
				},
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "nginx",
							"image": "nginx",
						},
					},
					"restartPolicy": "Always",
				},
			},
			field:   "object",
			wantErr: false,
		},
		{
			name: "invalid pod structure - typo in spec (strict)",
			obj: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata": map[string]interface{}{
					"name": "test-pod",
				},
				"spec": map[string]interface{}{
					"containerss": []interface{}{ // Typo 'containerss' instead of 'containers'
						map[string]interface{}{
							"name":  "nginx",
							"image": "nginx",
						},
					},
				},
			},
			field:   "object",
			wantErr: true, // Should fail with strict validation
		},
		{
			name: "invalid pod structure - wrong type for field",
			obj: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata": map[string]interface{}{
					"name": "test-pod",
				},
				"spec": map[string]interface{}{
					"restartPolicy": 123, // Should be string
				},
			},
			field:   "object",
			wantErr: true,
		},
		{
			name: "custom resource (unknown to scheme) - should pass leniently",
			obj: map[string]interface{}{
				"apiVersion": "cilium.io/v2",
				"kind":       "CiliumNetworkPolicy",
				"metadata": map[string]interface{}{
					"name": "rule1",
				},
				"spec": map[string]interface{}{
					"endpointSelector": map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"role": "backend",
						},
					},
				},
			},
			field:   "object",
			wantErr: false,
		},
		{
			name: "missing apiVersion",
			obj: map[string]interface{}{
				"kind": "Pod",
			},
			field:   "object",
			wantErr: true,
		},
		{
			name: "missing kind",
			obj: map[string]interface{}{
				"apiVersion": "v1",
			},
			field:   "object",
			wantErr: true,
		},
		{
			name: "wrong kind for namespaceObject",
			obj: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod", // Not Namespace
				"metadata": map[string]interface{}{
					"name": "foo",
				},
			},
			field: "namespaceObject",
			expectedGVK: &schema.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "Namespace",
			},
			wantErr: true,
		},
		{
			name: "correct kind for namespaceObject",
			obj: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Namespace",
				"metadata": map[string]interface{}{
					"name": "foo",
				},
			},
			field: "namespaceObject",
			expectedGVK: &schema.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "Namespace",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateWithScheme(tt.obj, tt.field, tt.expectedGVK)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateWithScheme() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

//nolint:funlen // Table-driven test, length is expected
func TestInferOperation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		hasObject    bool
		hasOldObject bool
		requestOpStr string
		want         string
		wantErr      bool
	}{
		{
			name:         "Explicit operation",
			hasObject:    true,
			hasOldObject: false,
			requestOpStr: "CONNECT",
			want:         "CONNECT",
			wantErr:      false,
		},
		{
			name:         "Create (Object only)",
			hasObject:    true,
			hasOldObject: false,
			requestOpStr: "",
			want:         "CREATE",
			wantErr:      false,
		},
		{
			name:         "Delete (OldObject only)",
			hasObject:    false,
			hasOldObject: true,
			requestOpStr: "",
			want:         "DELETE",
			wantErr:      false,
		},
		{
			name:         "Update (Both)",
			hasObject:    true,
			hasOldObject: true,
			requestOpStr: "",
			want:         "UPDATE",
			wantErr:      false,
		},
		{
			name:         "Error (Neither)",
			hasObject:    false,
			hasOldObject: false,
			requestOpStr: "",
			want:         "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := InferOperation(tt.hasObject, tt.hasOldObject, tt.requestOpStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("InferOperation() error = %v, wantErr %v", err, tt.wantErr)

				return
			}

			if got != tt.want {
				t.Errorf("InferOperation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseRequestYAML_Params(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	requestFile := filepath.Join(dir, "test.allow.request.yaml")
	content := `
operation: CREATE
object:
  apiVersion: v1
  kind: Pod
  metadata:
    name: test-pod
  spec:
    containers:
    - name: nginx
      image: nginx
params:
  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: my-config
  data:
    maxReplicas: "5"
`

	if err := os.WriteFile(requestFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	req := &testRequest{Name: "test.allow", FilePath: requestFile}

	data, err := os.ReadFile(requestFile)
	if err != nil {
		t.Fatal(err)
	}

	if err := parseRequestYAML(req, data); err != nil {
		t.Fatalf("parseRequestYAML() error = %v", err)
	}

	if req.Params == nil {
		t.Fatal("expected Params to be set, got nil")
	}

	if req.Params.GetName() != "my-config" {
		t.Errorf("Params.Name = %q, want %q", req.Params.GetName(), "my-config")
	}
}

//nolint:funlen // Test function length is due to YAML test data.
func TestParseRequestYAML_GoldFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	requestFile := filepath.Join(dir, "test.request.yaml")
	goldFile := filepath.Join(dir, "test.gold.yaml")

	requestContent := `
operation: CREATE
object:
  apiVersion: v1
  kind: Pod
  metadata:
    name: test-pod
  spec:
    containers:
    - name: nginx
      image: nginx
`
	goldContent := `
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
  labels:
    injected: "true"
spec:
  containers:
  - name: nginx
    image: nginx
`

	if err := os.WriteFile(requestFile, []byte(requestContent), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(goldFile, []byte(goldContent), 0o600); err != nil {
		t.Fatal(err)
	}

	req := &testRequest{Name: "test", FilePath: requestFile}

	data, err := os.ReadFile(requestFile)
	if err != nil {
		t.Fatal(err)
	}

	if err := parseRequestYAML(req, data); err != nil {
		t.Fatalf("parseRequestYAML() error = %v", err)
	}

	if !req.ExpectMutated {
		t.Error("expected ExpectMutated=true when .gold.yaml is present")
	}

	if req.ExpectedObject == nil {
		t.Fatal("expected ExpectedObject to be set")
	}

	if req.ExpectedObject.GetName() != "test-pod" {
		t.Errorf("ExpectedObject.Name = %q, want %q", req.ExpectedObject.GetName(), "test-pod")
	}
}
