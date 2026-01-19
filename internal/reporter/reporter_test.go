package reporter

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/zemanlx/kat/internal/evaluator"
)

func TestReporter_StartSuite(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	rep := New(buf)
	rep.SetFormat(FormatVerbose)

	rep.StartSuite("test-suite")

	output := buf.String()
	if !strings.Contains(output, "=== RUN   test-suite") {
		t.Errorf("Expected suite start output, got: %s", output)
	}
}

func TestReporter_StartTest(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	rep := New(buf)
	rep.SetFormat(FormatVerbose)

	s := rep.StartSuite("suite")
	s.StartTest("test")

	output := buf.String()
	if !strings.Contains(output, "=== RUN   suite/test") {
		t.Errorf("Expected test start output, got: %s", output)
	}

	total, _, _ := rep.Stats()
	if total != 1 {
		t.Errorf("Expected total tests to be 1, got %d", total)
	}
}

func TestReporter_ReportPass(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	rep := New(buf)
	rep.SetFormat(FormatVerbose)

	s := rep.StartSuite("suite")
	s.StartTest("test")
	s.ReportPass("test")

	output := buf.String()
	if !strings.Contains(output, "--- PASS: suite/test") {
		t.Errorf("Expected pass output, got: %s", output)
	}

	total, passed, failed := rep.Stats()
	if total != 1 || passed != 1 || failed != 0 {
		t.Errorf("Expected stats (1, 1, 0), got (%d, %d, %d)", total, passed, failed)
	}
}

func TestReporter_ReportFail(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	rep := New(buf)

	s := rep.StartSuite("suite")
	s.StartTest("test")
	s.ReportFail("test", "something went wrong")

	output := buf.String()
	if !strings.Contains(output, "--- FAIL: suite/test") {
		t.Errorf("Expected fail output, got: %s", output)
	}

	if !strings.Contains(output, "something went wrong") {
		t.Errorf("Expected failure message in output, got: %s", output)
	}

	total, passed, failed := rep.Stats()
	if total != 1 || passed != 0 || failed != 1 {
		t.Errorf("Expected stats (1, 0, 1), got (%d, %d, %d)", total, passed, failed)
	}
}

func TestReporter_ReportResult_Pass(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	rep := New(buf)
	rep.SetFormat(FormatVerbose)

	s := rep.StartSuite("suite")
	s.StartTest("test")

	result := &evaluator.TestResult{
		Passed: true,
	}
	s.ReportResult("test", result)

	output := buf.String()
	if !strings.Contains(output, "--- PASS: suite/test") {
		t.Errorf("Expected pass output, got: %s", output)
	}

	_, passed, _ := rep.Stats()
	if passed != 1 {
		t.Errorf("Expected 1 passed test, got %d", passed)
	}
}

func TestReporter_ReportResult_Fail(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	rep := New(buf)

	s := rep.StartSuite("suite")
	s.StartTest("test")

	result := &evaluator.TestResult{
		Passed:  false,
		Message: "validation failed",
	}
	s.ReportResult("test", result)

	output := buf.String()
	if !strings.Contains(output, "--- FAIL: suite/test") {
		t.Errorf("Expected fail output, got: %s", output)
	}

	if !strings.Contains(output, "validation failed") {
		t.Errorf("Expected failure message in output, got: %s", output)
	}

	_, _, failed := rep.Stats()
	if failed != 1 {
		t.Errorf("Expected 1 failed test, got %d", failed)
	}
}

func TestReporter_Summary_AllPass(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	rep := New(buf)
	rep.SetFormat(FormatVerbose)

	s := rep.StartSuite("suite")
	s.StartTest("test1")
	s.ReportPass("test1")
	s.StartTest("test2")
	s.ReportPass("test2")
	s.End()

	err := rep.Summary()
	if err != nil {
		t.Errorf("Expected no error for all passing tests, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "PASS") {
		t.Errorf("Expected PASS in summary, got: %s", output)
	}

	total, passed, _ := rep.Stats()
	if total != 2 || passed != 2 {
		t.Errorf("Expected stats (2, 2, 0), got (%d, %d)", total, passed)
	}
}

func TestReporter_Summary_WithFailures(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	rep := New(buf)
	rep.SetFormat(FormatVerbose)

	s := rep.StartSuite("suite")
	s.StartTest("test1")
	s.ReportPass("test1")
	s.StartTest("test2")
	s.ReportFail("test2", "failed")
	s.End()

	err := rep.Summary()
	if err == nil {
		t.Error("Expected error for failed tests")
	}

	if !errors.Is(err, errTestsFailed) {
		t.Errorf("Expected errTestsFailed sentinel, got: %v", err)
	}

	if !strings.Contains(err.Error(), "tests failed: 1") {
		t.Errorf("Expected 'tests failed: 1' in error, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "FAIL") {
		t.Errorf("Expected FAIL in summary, got: %s", output)
	}

	total, passed, failed := rep.Stats()
	if total != 2 || passed != 1 || failed != 1 {
		t.Errorf("Expected stats (2, 1, 1), got (%d, %d, %d)", total, passed, failed)
	}
}
