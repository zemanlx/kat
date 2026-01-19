package loader

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

//nolint:funlen // Test function
func TestInferOperation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		hasObject    bool
		hasOldObject bool
		requestOpStr string // operation in request.yaml, if any
		wantOp       string
		wantErr      bool
		wantErrMsg   string
	}{
		{
			name:         "infer CREATE from object only",
			hasObject:    true,
			hasOldObject: false,
			wantOp:       "CREATE",
			wantErr:      false,
		},
		{
			name:         "infer DELETE from oldObject only",
			hasObject:    false,
			hasOldObject: true,
			wantOp:       "DELETE",
			wantErr:      false,
		},
		{
			name:         "infer UPDATE from both object and oldObject",
			hasObject:    true,
			hasOldObject: true,
			wantOp:       "UPDATE",
			wantErr:      false,
		},
		{
			name:         "explicit CONNECT operation requires request.yaml",
			hasObject:    false,
			hasOldObject: false,
			requestOpStr: "CONNECT",
			wantOp:       "CONNECT",
			wantErr:      false,
		},
		{
			name:         "explicit CONNECT overrides inferred",
			hasObject:    true,
			hasOldObject: false,
			requestOpStr: "CONNECT",
			wantOp:       "CONNECT",
			wantErr:      false,
		},
		{
			name:         "no files and no explicit op is error",
			hasObject:    false,
			hasOldObject: false,
			wantErr:      true,
			wantErrMsg:   "cannot infer operation",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			op, err := InferOperation(tc.hasObject, tc.hasOldObject, tc.requestOpStr)

			if tc.wantErr {
				if err == nil {
					t.Errorf("got nil, want error")
				}

				return
			}

			if err != nil {
				t.Errorf("got error %v, want nil", err)

				return
			}

			if op != tc.wantOp {
				t.Errorf("got %q, want %q", op, tc.wantOp)
			}
		})
	}
}

//nolint:funlen,cyclop // Test function
func TestLoadYAML(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create test YAML files
	objectYAML := `apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: app
    image: nginx
`
	objectPath := filepath.Join(tmpDir, "test.object.yaml")

	if err := os.WriteFile(objectPath, []byte(objectYAML), 0o600); err != nil {
		t.Fatalf("write test object file: %v", err)
	}

	requestYAML := `operation: CREATE
userInfo:
  username: "user@example.com"
  groups: ["system:authenticated"]
namespace: default
`
	requestPath := filepath.Join(tmpDir, "test.request.yaml")

	if err := os.WriteFile(requestPath, []byte(requestYAML), 0o600); err != nil {
		t.Fatalf("write test request file: %v", err)
	}

	cases := []struct {
		name       string
		filePath   string
		wantErr    bool
		wantErrMsg string
		check      func(t *testing.T, data []byte)
	}{
		{
			name:     "load valid object.yaml",
			filePath: objectPath,
			wantErr:  false,
			check: func(t *testing.T, data []byte) {
				t.Helper()
				if !bytes.Contains(data, []byte("apiVersion: v1")) {
					t.Errorf("got data without apiVersion: v1: %s", data)
				}
				if !bytes.Contains(data, []byte("kind: Pod")) {
					t.Errorf("got data without kind: Pod: %s", data)
				}
			},
		},
		{
			name:     "load valid request.yaml",
			filePath: requestPath,
			wantErr:  false,
			check: func(t *testing.T, data []byte) {
				t.Helper()
				if !bytes.Contains(data, []byte("operation: CREATE")) {
					t.Errorf("got data without operation: CREATE: %s", data)
				}
			},
		},
		{
			name:       "nonexistent file returns error",
			filePath:   filepath.Join(tmpDir, "nonexistent.yaml"),
			wantErr:    true,
			wantErrMsg: "no such file",
		},
		{
			name:       "invalid YAML returns error",
			filePath:   tmpDir, // passing directory instead of file
			wantErr:    true,
			wantErrMsg: "is a directory",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := LoadYAML(tc.filePath)

			if tc.wantErr {
				if err == nil {
					t.Errorf("got nil, want error")
				}

				return
			}

			if err != nil {
				t.Errorf("got error %v, want nil", err)

				return
			}

			if tc.check != nil {
				tc.check(t, data)
			}
		})
	}
}
