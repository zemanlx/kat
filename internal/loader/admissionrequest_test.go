package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	admission "k8s.io/api/admission/v1"
)

//nolint:funlen // Test function
func TestBuildAdmissionRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		operation   string
		objectBytes []byte
		oldObjBytes []byte
		requestYAML []byte
		wantOp      string
		wantErr     bool
	}{
		{
			name:      "create pod from object",
			operation: "CREATE",
			objectBytes: []byte(`apiVersion: v1
kind: Pod
metadata:
  name: test
`),
			wantOp:  "CREATE",
			wantErr: false,
		},
		{
			name:      "delete pod from oldObject",
			operation: "DELETE",
			oldObjBytes: []byte(`apiVersion: v1
kind: Pod
metadata:
  name: test
`),
			wantOp:  "DELETE",
			wantErr: false,
		},
		{
			name:      "update pod with both object and oldObject",
			operation: "UPDATE",
			objectBytes: []byte(`apiVersion: v1
kind: Pod
spec:
  replicas: 2
`),
			oldObjBytes: []byte(`apiVersion: v1
kind: Pod
spec:
  replicas: 1
`),
			wantOp:  "UPDATE",
			wantErr: false,
		},
		{
			name:        "connect with subResource",
			operation:   "CONNECT",
			requestYAML: []byte("subResource: exec\n"),
			wantOp:      "CONNECT",
			wantErr:     false,
		},
		{
			name:        "nil objectBytes for CREATE is error",
			operation:   "CREATE",
			objectBytes: nil,
			wantErr:     true,
		},
		{
			name:        "nil oldObjBytes for DELETE is error",
			operation:   "DELETE",
			oldObjBytes: nil,
			wantErr:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := BuildAdmissionRequest(tc.operation, tc.objectBytes, tc.oldObjBytes, tc.requestYAML)

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

			if req.Operation != admission.Operation(tc.wantOp) {
				t.Errorf("got Operation %q, want %q", req.Operation, tc.wantOp)
			}
		})
	}
}

//nolint:funlen // Test function
func TestParseRequestYAML(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		yamlBytes []byte
		wantUser  string
		wantNS    string
		wantErr   bool
	}{
		{
			name: "parse basic request fields",
			yamlBytes: []byte(`userInfo:
  username: alice
namespace: default
`),
			wantUser: "alice",
			wantNS:   "default",
			wantErr:  false,
		},
		{
			name:      "empty YAML is OK (zero value)",
			yamlBytes: []byte(""),
			wantUser:  "",
			wantNS:    "",
			wantErr:   false,
		},
		{
			name: "parse groups and uid",
			yamlBytes: []byte(`userInfo:
  username: bob
  uid: "12345"
  groups: ["developers", "admins"]
`),
			wantUser: "bob",
			wantErr:  false,
		},
		{
			name: "parse all request fields",
			yamlBytes: []byte(`operation: CREATE
userInfo:
  username: alice
  uid: "123"
  groups: ["admins"]
namespace: prod
subResource: exec
dryRun: true
name: test-pod
`),
			wantUser: "alice",
			wantNS:   "prod",
			wantErr:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := parseAdmissionRequestYAML(tc.yamlBytes)

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

			if req.UserInfo.Username != tc.wantUser {
				t.Errorf("got Username %q, want %q", req.UserInfo.Username, tc.wantUser)
			}

			if req.Namespace != tc.wantNS {
				t.Errorf("got Namespace %q, want %q", req.Namespace, tc.wantNS)
			}
		})
	}
}

// reqSnapshot captures the fields we care to assert in golden tests.
type reqSnapshot struct {
	Operation   string `json:"operation"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	UID         string `json:"uid"`
	SubResource string `json:"subResource"`
	DryRun      bool   `json:"dryRun"`
	Kind        struct {
		Group   string `json:"group"`
		Version string `json:"version"`
		Kind    string `json:"kind"`
	} `json:"kind"`
	Resource struct {
		Group    string `json:"group"`
		Version  string `json:"version"`
		Resource string `json:"resource"`
	} `json:"resource"`
	UserInfo struct {
		Username string   `json:"username"`
		Groups   []string `json:"groups"`
	} `json:"userInfo"`
}

func snapshotFromRequest(req *admission.AdmissionRequest) reqSnapshot {
	var s reqSnapshot
	s.Operation = string(req.Operation)
	s.Name = req.Name
	s.Namespace = req.Namespace
	s.UID = string(req.UID)
	s.SubResource = req.SubResource
	s.DryRun = req.DryRun != nil && *req.DryRun
	s.Kind.Group = req.Kind.Group
	s.Kind.Version = req.Kind.Version
	s.Kind.Kind = req.Kind.Kind
	s.Resource.Group = req.Resource.Group
	s.Resource.Version = req.Resource.Version
	s.Resource.Resource = req.Resource.Resource
	s.UserInfo.Username = req.UserInfo.Username
	// Initialize Groups as empty slice instead of nil for consistent comparison
	if req.UserInfo.Groups == nil {
		s.UserInfo.Groups = []string{}
	} else {
		s.UserInfo.Groups = append([]string(nil), req.UserInfo.Groups...)
	}

	return s
}

// TestAdmissionRequest_Golden loads fixtures from testdata and compares to golden snapshots.
//

//nolint:funlen,cyclop // Test function
func TestAdmissionRequest_Golden(t *testing.T) {
	t.Parallel()

	root := "admissionrequest_testdata"
	cases := []struct {
		name string
		dir  string
	}{
		{name: "create infer from object", dir: "create_infer"},
		{name: "create request overrides metadata", dir: "create_override"},
		{name: "update both objects request overrides", dir: "update_override"},
		{name: "delete infer kind/resource with request override name", dir: "delete_override"},
		{name: "connect exec request-only", dir: "connect_exec"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(root, tc.dir)

			var objectBytes, oldObjBytes, requestYAML []byte

			if b, err := os.ReadFile(filepath.Join(dir, "object.yaml")); err == nil {
				objectBytes = b
			}

			if b, err := os.ReadFile(filepath.Join(dir, "oldObject.yaml")); err == nil {
				oldObjBytes = b
			}

			if b, err := os.ReadFile(filepath.Join(dir, "request.yaml")); err == nil {
				requestYAML = b
			}

			// Infer operation from which files exist
			op := "CREATE"
			if len(oldObjBytes) > 0 && len(objectBytes) > 0 {
				op = "UPDATE"
			} else if len(oldObjBytes) > 0 {
				op = "DELETE"
			}

			req, err := BuildAdmissionRequest(op, objectBytes, oldObjBytes, requestYAML)
			if err != nil {
				t.Fatalf("BuildAdmissionRequest error: %v", err)
			}

			got := snapshotFromRequest(req)

			wantBytes, err := os.ReadFile(filepath.Join(dir, "expected.json"))
			if err != nil {
				t.Fatalf("read expected.json: %v", err)
			}

			var want reqSnapshot
			if err := json.Unmarshal(wantBytes, &want); err != nil {
				t.Fatalf("unmarshal expected.json: %v", err)
			}

			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("admission request snapshot mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
