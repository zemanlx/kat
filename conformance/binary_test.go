package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// katEvent is one line of `kat -json` output.
type katEvent struct {
	Action string `json:"action"`
	Test   string `json:"test"`
	Output string `json:"output"`
}

// binaryBaseName is the generated test's file base name. The case name's dots
// are replaced so that its tokens are not read as an expectation.
func (c *katCase) binaryBaseName() string {
	expect := "allow"
	if !c.run.server.allowed {
		expect = "deny"
	}

	return c.policyName + "." + strings.ReplaceAll(c.name, ".", "_") + "." + expect
}

// inBinarySuite reports whether the case goes into the generated suite: only
// cases that match the server at library level, so each divergence is
// reported once, and only decisions a kat test file can express.
func (c *katCase) inBinarySuite() bool {
	return !c.isInconclusive() && c.run.diff == "" && c.run.server.policyErr == ""
}

// writeBinarySuite writes the shard's cases as a kat suite whose expectations
// are the server's results.
func (h *shardRun) writeBinarySuite(dir string) error {
	testsDir := filepath.Join(dir, "tests")
	if err := os.MkdirAll(testsDir, 0o750); err != nil {
		return fmt.Errorf("create suite directory: %w", err)
	}

	if err := copyPolicyFiles(h.suite.Path, dir); err != nil {
		return err
	}

	for _, c := range h.shard.cases {
		if !c.inBinarySuite() {
			continue
		}

		if err := writeBinaryCase(testsDir, c); err != nil {
			return fmt.Errorf("write %s: %w", c.name, err)
		}

		c.run.binaryRan = true
	}

	return nil
}

func copyPolicyFiles(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return fmt.Errorf("read suite directory: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() || !isPolicyOrBindingFile(e.Name()) {
			continue
		}

		data, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}

		//nolint:gosec // The name comes from listing the fixture suite directory.
		if err := os.WriteFile(filepath.Join(to, e.Name()), data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", e.Name(), err)
		}
	}

	return nil
}

// isPolicyOrBindingFile mirrors the loader's policy and binding file names.
func isPolicyOrBindingFile(name string) bool {
	for _, base := range []string{"policy", "policies", "binding", "bindings"} {
		for _, ext := range []string{".yaml", ".yml"} {
			if name == base+ext || strings.HasSuffix(name, "."+base+ext) {
				return true
			}
		}
	}

	return false
}

// writeBinaryCase writes the normalized inputs as a .request.yaml and the
// server's result as the expectation files.
func writeBinaryCase(dir string, c *katCase) error {
	base := filepath.Join(dir, c.binaryBaseName())

	files, err := binaryCaseFiles(c)
	if err != nil {
		return err
	}

	for suffix, content := range files {
		if err := os.WriteFile(base+suffix, content, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", suffix, err)
		}
	}

	return nil
}

// binaryCaseFiles returns the case's files by suffix.
func binaryCaseFiles(c *katCase) (map[string][]byte, error) {
	run := &c.run
	yamlFiles := map[string]any{".request.yaml": binaryRequest(c)}

	if len(c.tc.Authorizer) > 0 {
		yamlFiles[".authorizer.yaml"] = c.tc.Authorizer
	}

	// Written even when empty, so kat also asserts the server's lack of them.
	yamlFiles[".annotations.yaml"] = map[string]string{}
	if run.server.annotations != nil {
		yamlFiles[".annotations.yaml"] = run.server.annotations
	}

	if run.katResult != nil && run.katResult.PatchedObject != nil {
		yamlFiles[".gold.yaml"] = run.katResult.PatchedObject.Object
	}

	files := make(map[string][]byte, len(yamlFiles)+2)

	for suffix, content := range yamlFiles {
		data, err := yaml.Marshal(content)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", suffix, err)
		}

		files[suffix] = data
	}

	if !run.server.allowed && run.server.message != "" {
		files[".message.txt"] = []byte(run.server.message + "\n")
	}

	files[".warnings.txt"] = []byte(strings.Join(run.server.warnings, "\n") + "\n")

	return files, nil
}

// binaryRequest is the case's .request.yaml: the same normalized inputs as
// the library-level comparison.
func binaryRequest(c *katCase) map[string]any {
	run := &c.run
	req := map[string]any{
		"operation": string(c.op),
		"name":      c.objName,
		"userInfo":  run.request.UserInfo,
	}

	if c.namespaced {
		req["namespace"] = c.namespace
	}

	if c.op == admissionv1.Connect {
		// kat infers the operation from object presence, so the
		// PodExecOptions object cannot be expressed here.
		req["resource"] = map[string]any{"group": c.gvr.Group, "version": c.gvr.Version, "resource": c.gvr.Resource}
		req["subResource"] = c.subresource
	} else {
		req["options"] = requestOptions(c.op)
		req["dryRun"] = true
	}

	for key, u := range map[string]*unstructured.Unstructured{
		"object":    objectIfNotConnect(c),
		"oldObject": run.oldObject,
		"params":    run.params,
	} {
		if u != nil {
			req[key] = u.Object
		}
	}

	if run.namespaceObj != nil {
		req["namespaceObject"] = namespaceWithType(run.namespaceObj).Object
	}

	return req
}

func objectIfNotConnect(c *katCase) *unstructured.Unstructured {
	if c.op == admissionv1.Connect {
		return nil
	}

	return c.run.object
}

var errKatExit = errors.New("kat exited with an error")

// runBinary runs the kat binary on the generated suite and attributes its
// failures to the cases.
func (h *shardRun) runBinary(ctx context.Context, dir string) {
	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, katBinary, "-json", "-k8s-version", h.k8sVersion, dir) //nolint:gosec // The harness builds kat and the suite.
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	results, outputs := parseKatEvents(stdout.Bytes())
	anyFailed := false

	for _, c := range h.shard.cases {
		if !c.run.binaryRan {
			continue
		}

		test := c.binaryBaseName() + ".yaml"

		switch results[test] {
		case "pass":
		case "fail":
			anyFailed = true
			c.run.binaryFailure = fmt.Sprintf("kat failed the case generated from the server's result (%s):\n%s",
				filepath.Join(dir, "tests", c.binaryBaseName()), outputs[test])
		default:
			c.run.binaryFailure = fmt.Sprintf("kat did not report %s: %v\nstdout:\n%s\nstderr:\n%s",
				test, runErr, stdout.String(), stderr.String())
		}
	}

	// A failing exit status must come from a failed case.
	if runErr != nil && !anyFailed {
		h.failBinary(fmt.Sprintf("%v: %v\nstderr:\n%s", errKatExit, runErr, stderr.String()))
	}
}

// failBinary fails every generated case that kat did not already fail.
func (h *shardRun) failBinary(msg string) {
	for _, c := range h.shard.cases {
		if c.run.binaryRan && c.run.binaryFailure == "" {
			c.run.binaryFailure = msg
		}
	}
}

func parseKatEvents(out []byte) (map[string]string, map[string]string) {
	results := map[string]string{}
	outputs := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<24)

	for scanner.Scan() {
		var e katEvent
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.Test == "" {
			continue
		}

		switch e.Action {
		case "pass", "fail":
			results[e.Test] = e.Action
		case "output":
			outputs[e.Test] += e.Output
		}
	}

	return results, outputs
}
