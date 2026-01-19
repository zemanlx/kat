package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

//nolint:gochecknoglobals // Test flag
var update = flag.Bool("update", false, "update golden files")

type runTestSpec struct {
	name    string
	args    []string
	golden  string
	wantErr bool
}

func TestRun(t *testing.T) {
	t.Parallel()

	// Ensure we are in the project root for tests that use relative paths
	// In some environments go test runs in the package directory.
	// We assume 'kat' is in the root of the repo.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Simple check to ensure we are running where we expect
	if _, err := os.Stat("test-policies-pass"); os.IsNotExist(err) {
		t.Fatalf("test-policies-pass directory not found in %s. Run tests from project root.", wd)
	}

	tests := []runTestSpec{
		{
			name:   "AllPolicies",
			args:   []string{"kat", "test-policies-pass"},
			golden: "testdata/all_policies.golden",
		},
		{
			name:   "SpecificDirectoryMutating",
			args:   []string{"kat", "test-policies-pass/mutating"},
			golden: "testdata/specific_dir_mutating.golden",
		},
		{
			name:    "RecursionFromDot",
			args:    []string{"kat", "."},
			golden:  "testdata/recursion_dot.golden",
			wantErr: true,
		},
		{
			name:   "RunRegex",
			args:   []string{"kat", "-run", "limit.*params", "test-policies-pass"},
			golden: "testdata/regex_limit_params.golden",
		},
		{
			name:    "FailPolicies",
			args:    []string{"kat", "test-policies-fail"},
			golden:  "testdata/fail_policies.golden",
			wantErr: true,
		},
		{
			name:   "JSONOutput",
			args:   []string{"kat", "-json", "test-policies-pass/mutating"},
			golden: "testdata/json_output.golden",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runTestCase(t, tt)
		})
	}
}

func runTestCase(t *testing.T, tt runTestSpec) {
	t.Helper()

	r, w, _ := os.Pipe()

	// Capture stdout
	// We pass a dummy function for getenv to ensure determinism if needed,
	// but os.Getenv is fine as long as tests don't depend on env vars unless specified.
	mockGetenv := func(_ string) string { return "" }

	err := run(t.Context(), tt.args, mockGetenv, os.Stdin, w)
	w.Close()

	if (err != nil) != tt.wantErr {
		t.Fatalf("run() error = %v, wantErr %v", err, tt.wantErr)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read output: %v", err)
	}

	got := sanitizeOutput(string(out))
	goldenPath := tt.golden

	if *update {
		errCases := os.MkdirAll(filepath.Dir(goldenPath), 0o755)
		if errCases != nil {
			t.Fatal(errCases)
		}

		errCases = os.WriteFile(goldenPath, []byte(got), 0o600)
		if errCases != nil {
			t.Fatal(errCases)
		}
	}

	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		// If golden file missing and not updating, fail
		t.Fatalf("failed to read golden file %s: %v", goldenPath, err)
	}

	want := string(wantBytes)

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Mismatch (-want +got):\n%s", diff)
	}
}

var (
	durationRegex      = regexp.MustCompile(`\(\d+\.\d+s\)`)
	suiteDurationRegex = regexp.MustCompile(`\t\d+\.\d+s`)
	jsonTimeRegex      = regexp.MustCompile(`"time":"[^"]+"`)
	elapsedRegex       = regexp.MustCompile(`"elapsed":[\d\.]+`)
)

func sanitizeOutput(output string) string {
	// Replace (0.00s) with (0.00s) to normalize checks
	// Actually, replace with fixed string
	output = durationRegex.ReplaceAllString(output, "(0.00s)")
	// Replace tab separated durations in suite summary
	output = suiteDurationRegex.ReplaceAllString(output, "\t0.000s")
	// Replace JSON timestamps
	output = jsonTimeRegex.ReplaceAllString(output, `"time":"2000-01-01T00:00:00Z"`)
	// Replace JSON elapsed
	output = elapsedRegex.ReplaceAllString(output, `"elapsed":0`)

	// Normalize paths in output if they appear (e.g. windows vs linux)
	// Kat seems to output suite names which are derived from paths.
	// If paths are absolute, sanitize them.
	// Based on loader.go, suite name is filepath.Base(path) or derived from directory structure.

	// Also ensure consistent newlines
	output = strings.ReplaceAll(output, "\r\n", "\n")

	return output
}
