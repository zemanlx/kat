package ssa

import (
	_ "embed"
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// schemaVersion is the k8s.io module version the embedded schema was generated
// for; the drift guard below checks it against the linked dependency.
//
//go:embed schema.version
var schemaVersion string

// TestSchemaVersionMatchesDependency fails if the embedded OpenAPI schema was
// generated for a different k8s.io release than the one this binary links
// against. Regenerate with hack/update-schema.sh after bumping k8s.io/* deps.
func TestSchemaVersionMatchesDependency(t *testing.T) {
	t.Parallel()

	want := strings.TrimSpace(schemaVersion)
	got := moduleVersion(t, "k8s.io/apimachinery")

	if got != want {
		t.Fatalf(
			"embedded schema version %q != k8s.io/apimachinery %q; "+
				"run hack/update-schema.sh after bumping k8s.io/* dependencies",
			want, got,
		)
	}
}

// moduleVersion returns the module version of path from the test binary's build
// info, following any replace directive.
func moduleVersion(t *testing.T, path string) string {
	t.Helper()

	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("no build info available")
	}

	for _, dep := range info.Deps {
		if dep.Path != path {
			continue
		}

		if dep.Replace != nil {
			return dep.Replace.Version
		}

		return dep.Version
	}

	t.Fatalf("dependency %q not found in build info", path)

	return ""
}

// normalize round-trips obj through JSON so numeric types (int64 vs float64)
// match regardless of how the value was produced, making deep comparison stable.
func normalize(t *testing.T, obj map[string]any) map[string]any {
	t.Helper()

	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	return out
}

//nolint:funlen // Table-driven test.
func TestMerge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		live    map[string]any
		patch   map[string]any
		want    map[string]any
		wantErr bool
	}{
		{
			name: "containers merged by name, other container and fields preserved",
			live: pod(map[string]any{
				"containers": []any{
					map[string]any{
						"name": "app", "image": "app:1",
						"env":   []any{map[string]any{"name": "E1", "value": "v1"}},
						"ports": []any{map[string]any{"containerPort": int64(80), "protocol": "TCP"}},
					},
					map[string]any{"name": "side", "image": "side:1"},
				},
			}),
			patch: podSpec(map[string]any{
				"containers": []any{map[string]any{"name": "app", "image": "app:2"}},
			}),
			want: pod(map[string]any{
				"containers": []any{
					map[string]any{
						"name": "app", "image": "app:2",
						"env":   []any{map[string]any{"name": "E1", "value": "v1"}},
						"ports": []any{map[string]any{"containerPort": int64(80), "protocol": "TCP"}},
					},
					map[string]any{"name": "side", "image": "side:1"},
				},
			}),
		},
		{
			name: "env list merged by name within a container",
			live: pod(map[string]any{
				"containers": []any{map[string]any{
					"name": "app", "image": "app:1",
					"env": []any{map[string]any{"name": "E1", "value": "v1"}},
				}},
			}),
			patch: podSpec(map[string]any{
				"containers": []any{map[string]any{
					"name": "app",
					"env":  []any{map[string]any{"name": "E2", "value": "v2"}},
				}},
			}),
			want: pod(map[string]any{
				"containers": []any{map[string]any{
					"name": "app", "image": "app:1",
					"env": []any{
						map[string]any{"name": "E1", "value": "v1"},
						map[string]any{"name": "E2", "value": "v2"},
					},
				}},
			}),
		},
		{
			name:  "set list (finalizers) is unioned",
			live:  podMeta(map[string]any{"name": "p", "finalizers": []any{"a"}}),
			patch: map[string]any{"metadata": map[string]any{"finalizers": []any{"b"}}},
			want:  podMeta(map[string]any{"name": "p", "finalizers": []any{"a", "b"}}),
		},
		{
			name:  "labels map is merged",
			live:  podMeta(map[string]any{"name": "p", "labels": map[string]any{"a": "1"}}),
			patch: map[string]any{"metadata": map[string]any{"labels": map[string]any{"b": "2"}}},
			want:  podMeta(map[string]any{"name": "p", "labels": map[string]any{"a": "1", "b": "2"}}),
		},
		{
			name: "atomic list (command) is replaced wholesale",
			live: pod(map[string]any{
				"containers": []any{map[string]any{
					"name": "app", "image": "app:1",
					"command": []any{"old1", "old2"},
				}},
			}),
			patch: podSpec(map[string]any{
				"containers": []any{map[string]any{
					"name": "app", "command": []any{"new"},
				}},
			}),
			want: pod(map[string]any{
				"containers": []any{map[string]any{
					"name": "app", "image": "app:1",
					"command": []any{"new"},
				}},
			}),
		},
		{
			name: "unknown kind falls back to schemaless merge",
			live: map[string]any{
				"apiVersion": "example.com/v1", "kind": "Widget",
				"metadata": map[string]any{"name": "w"},
				"spec":     map[string]any{"replicas": int64(1), "items": []any{map[string]any{"name": "a"}}},
			},
			patch: map[string]any{"spec": map[string]any{"replicas": int64(2)}},
			want: map[string]any{
				"apiVersion": "example.com/v1", "kind": "Widget",
				"metadata": map[string]any{"name": "w"},
				"spec":     map[string]any{"replicas": int64(2), "items": []any{map[string]any{"name": "a"}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Merge(
				&unstructured.Unstructured{Object: tt.live},
				&unstructured.Unstructured{Object: tt.patch},
			)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Merge() error = nil, want error")
				}

				return
			}

			if err != nil {
				t.Fatalf("Merge() error = %v, want nil", err)
			}

			if diff := cmp.Diff(normalize(t, tt.want), normalize(t, got.Object)); diff != "" {
				t.Errorf("Merge() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// pod builds a minimal Pod object with the given spec.
func pod(s map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": "p"},
		"spec":       s,
	}
}

// podSpec builds a GVK-less patch that sets only the pod spec, mirroring how an
// ApplyConfiguration expression omits apiVersion/kind.
func podSpec(s map[string]any) map[string]any {
	return map[string]any{"spec": s}
}

// podMeta builds a Pod object carrying only the given metadata.
func podMeta(meta map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   meta,
	}
}
