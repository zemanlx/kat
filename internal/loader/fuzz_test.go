package loader

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fuzzFilePath names no file, so the gold and message lookups find nothing.
const fuzzFilePath = "fuzz/input.request.yaml"

// globSeeds returns the paths matched by each pattern. It fails the test when a
// pattern matches nothing, so a moved fixture tree cannot empty a corpus silently.
func globSeeds(f *testing.F, patterns ...string) []string {
	f.Helper()

	var paths []string

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			f.Fatalf("glob %s: %v", pattern, err)
		}

		if len(matches) == 0 {
			f.Fatalf("no seed files match %s", pattern)
		}

		paths = append(paths, matches...)
	}

	return paths
}

func addFileSeeds(f *testing.F, patterns ...string) {
	f.Helper()

	for _, path := range globSeeds(f, patterns...) {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatalf("read seed %s: %v", path, err)
		}

		f.Add(data)
	}
}

func newFuzzRequest() *testRequest {
	return &testRequest{Name: "fuzz.allow", FilePath: fuzzFilePath}
}

// FuzzParseRequestYAML checks that a successful parse always builds a Request.
func FuzzParseRequestYAML(f *testing.F) {
	addFileSeeds(f,
		"admissionrequest_testdata/*/request.yaml",
		"../../test-policies-pass/*/*/tests/*.request.yaml",
	)

	f.Fuzz(func(t *testing.T, data []byte) {
		testReq := newFuzzRequest()

		if err := parseRequestYAML(testReq, data); err == nil && testReq.Request == nil {
			t.Fatal("parseRequestYAML succeeded without setting Request")
		}
	})
}

// FuzzObjectYAML runs the object, oldObject, and namespaceObject parsers on one
// input. Each one must set the field it owns whenever it reports success.
func FuzzObjectYAML(f *testing.F) {
	addFileSeeds(f,
		"admissionrequest_testdata/*/object.yaml",
		"admissionrequest_testdata/*/oldObject.yaml",
		"../../test-policies-pass/*/*/tests/*.object.yaml",
		"../../test-policies-pass/*/*/tests/*.oldObject.yaml",
		"../../test-policies-pass/*/*/tests/*.namespaceObject.yaml",
	)

	f.Fuzz(func(t *testing.T, data []byte) {
		object := newFuzzRequest()
		if err := parseObjectYAML(object, data); err == nil && (object.Object == nil || object.Request == nil) {
			t.Fatal("parseObjectYAML succeeded without setting Object and Request")
		}

		oldObject := newFuzzRequest()
		if err := parseOldObjectYAML(oldObject, data); err == nil && (oldObject.OldObject == nil || oldObject.Request == nil) {
			t.Fatal("parseOldObjectYAML succeeded without setting OldObject and Request")
		}

		namespace := newFuzzRequest()
		if err := parseNamespaceObjectYAML(namespace, data); err == nil && namespace.NamespaceObj == nil {
			t.Fatal("parseNamespaceObjectYAML succeeded without setting NamespaceObj")
		}
	})
}

// FuzzPolicyYAML loads the same bytes into two fresh PolicySets. Loading is pure,
// so both runs must agree on the error and on how many resources were loaded.
func FuzzPolicyYAML(f *testing.F) {
	addFileSeeds(f,
		"testdata/*/*.yaml",
		"../../test-policies-pass/*/*/policy.yaml",
		"../../test-policies-pass/*/*/binding.yaml",
	)

	f.Fuzz(func(t *testing.T, data []byte) {
		first := &PolicySet{}
		firstErr := first.loadDocuments(data, fuzzFilePath)

		second := &PolicySet{}
		secondErr := second.loadDocuments(data, fuzzFilePath)

		if (firstErr == nil) != (secondErr == nil) ||
			(firstErr != nil && firstErr.Error() != secondErr.Error()) ||
			loadedCount(first) != loadedCount(second) {
			t.Fatalf("loadDocuments is not deterministic: %v (%d loaded) vs %v (%d loaded)",
				firstErr, loadedCount(first), secondErr, loadedCount(second))
		}
	})
}

func loadedCount(ps *PolicySet) int {
	return len(ps.MutatingPolicies) + len(ps.MutatingBindings) +
		len(ps.ValidatingPolicies) + len(ps.ValidatingBindings)
}

// FuzzTestFileName checks the filename grammar on arbitrary names.
func FuzzTestFileName(f *testing.F) {
	for _, path := range globSeeds(f, "../../test-policies-pass/*/*/tests/*") {
		f.Add(filepath.Base(path))
	}

	f.Fuzz(func(t *testing.T, name string) {
		base := testBaseName(name)

		checkTestFileGrammar(t, name, base)
		checkMatchPolicyName(t, base, dotPrefixes(base))
	})
}

func checkTestFileGrammar(t *testing.T, name, base string) {
	t.Helper()

	if isTestFile(name) != (base != name) {
		t.Fatalf("isTestFile(%q) disagrees with testBaseName, which returned %q", name, base)
	}

	if isTestFile(name) && !strings.HasPrefix(name, base) {
		t.Fatalf("testBaseName(%q) = %q is not a prefix of the name", name, base)
	}

	denied := strings.Contains(base, ".deny.") || strings.HasSuffix(base, ".deny")
	if denied && expectedAllowed(base) {
		t.Fatalf("expectedAllowed(%q) = true for a deny case", base)
	}
}

// dotPrefixes returns the non-empty dot-separated prefixes of base. They stand in
// for policy names, so the prefix match and the single-policy fallback both run.
// An empty name is skipped because the empty prefix would match every base.
func dotPrefixes(base string) []string {
	var prefixes []string

	for i, r := range base {
		if r == '.' && i > 0 {
			prefixes = append(prefixes, base[:i])
		}
	}

	return prefixes
}

func checkMatchPolicyName(t *testing.T, base string, candidates []string) {
	t.Helper()

	got := matchPolicyName(base, candidates)
	if got != "" && !slices.Contains(candidates, got) {
		t.Fatalf("matchPolicyName(%q) = %q, not one of %q", base, got, candidates)
	}

	matched := slices.ContainsFunc(candidates, func(c string) bool {
		return strings.HasPrefix(base, c+".")
	})

	if matched && (got == "" || !strings.HasPrefix(base, got+".")) {
		t.Fatalf("matchPolicyName(%q) = %q, but a policy name prefixes the base", base, got)
	}

	if !matched && len(candidates) > 1 && got != "" {
		t.Fatalf("matchPolicyName(%q) = %q with %d policies and no prefix match", base, got, len(candidates))
	}
}
