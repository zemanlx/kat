package loader

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	admissionv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
)

// reuse a single universal deserializer across calls.
//
//nolint:gochecknoglobals // Used across the package for deserialization
var universalDeserializer = serializer.NewCodecFactory(scheme.Scheme).UniversalDeserializer()

// PolicySet contains policies and bindings loaded from a directory.
type PolicySet struct {
	Dir                string
	MutatingPolicies   []*admissionv1beta1.MutatingAdmissionPolicy
	MutatingBindings   []*admissionv1beta1.MutatingAdmissionPolicyBinding
	ValidatingPolicies []*admissionv1.ValidatingAdmissionPolicy
	ValidatingBindings []*admissionv1.ValidatingAdmissionPolicyBinding
}

// Skips directories: tests, testdata, .git, and any starting with '.'.
//
//nolint:cyclop // Directory walk needs several conditional exits
func LoadPolicySet(dir string) (*PolicySet, error) {
	ps := &PolicySet{Dir: dir}

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}

		// Skip special directories
		if d.IsDir() {
			name := d.Name()
			if name == "tests" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}

			return nil
		}

		// Check if file matches policy or binding naming convention
		name := d.Name()
		if !isPolicyFile(name) && !isBindingFile(name) {
			return nil
		}

		// Read and load the file
		fileBytes, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		// Process all documents in the YAML file
		if err := ps.loadDocuments(fileBytes, path); err != nil {
			return fmt.Errorf("load documents from %s: %w", path, err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk policy dir %s: %w", dir, err)
	}

	return ps, nil
}

// Matches: policy.yaml, policies.yaml, *.policy.yaml, *.policies.yaml.
func isPolicyFile(name string) bool {
	return name == "policy.yaml" || name == "policy.yml" ||
		name == "policies.yaml" || name == "policies.yml" ||
		strings.HasSuffix(name, ".policy.yaml") || strings.HasSuffix(name, ".policy.yml") ||
		strings.HasSuffix(name, ".policies.yaml") || strings.HasSuffix(name, ".policies.yml")
}

// Matches: binding.yaml, bindings.yaml, *.binding.yaml, *.bindings.yaml.
func isBindingFile(name string) bool {
	return name == "binding.yaml" || name == "binding.yml" ||
		name == "bindings.yaml" || name == "bindings.yml" ||
		strings.HasSuffix(name, ".binding.yaml") || strings.HasSuffix(name, ".binding.yml") ||
		strings.HasSuffix(name, ".bindings.yaml") || strings.HasSuffix(name, ".bindings.yml")
}

// loadDocuments splits a YAML file into documents and loads each one.
//
//nolint:cyclop // Multiple document decoding and type dispatch
func (ps *PolicySet) loadDocuments(yamlBytes []byte, filePath string) error {
	// Use yaml.Decoder to handle multi-document YAML files
	dec := yaml.NewDecoder(bytes.NewReader(yamlBytes))

	docNum := 1

	for {
		var node yaml.Node

		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fmt.Errorf("decode document %d: %w", docNum, err)
		}

		// Convert YAML node directly to JSON
		jsonBytes, err := yamlNodeToJSON(&node)
		if err != nil {
			return fmt.Errorf("convert document %d to JSON: %w", docNum, err)
		}

		// Decode once and append based on concrete type
		obj, _, err := universalDeserializer.Decode(jsonBytes, nil, nil)
		if err != nil {
			return fmt.Errorf("decode document %d: %w", docNum, err)
		}

		switch o := obj.(type) {
		case *admissionv1beta1.MutatingAdmissionPolicy:
			ps.MutatingPolicies = append(ps.MutatingPolicies, o)
		case *admissionv1beta1.MutatingAdmissionPolicyBinding:
			ps.MutatingBindings = append(ps.MutatingBindings, o)
		case *admissionv1.ValidatingAdmissionPolicy:
			ps.ValidatingPolicies = append(ps.ValidatingPolicies, o)
		case *admissionv1.ValidatingAdmissionPolicyBinding:
			ps.ValidatingBindings = append(ps.ValidatingBindings, o)
		case *admissionv1beta1.ValidatingAdmissionPolicy:
			return fmt.Errorf("%w: document %d in %s", ErrUnsupportedV1Beta1Policy, docNum, filePath)
		case *admissionv1beta1.ValidatingAdmissionPolicyBinding:
			return fmt.Errorf("%w: document %d in %s", ErrUnsupportedV1Beta1Binding, docNum, filePath)
		}

		docNum++
	}

	return nil
}

// yamlNodeToJSON converts a YAML node to JSON bytes.
func yamlNodeToJSON(node *yaml.Node) ([]byte, error) {
	var data any
	if err := node.Decode(&data); err != nil {
		return nil, fmt.Errorf("decode YAML node: %w", err)
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON: %w", err)
	}

	return jsonBytes, nil
}
