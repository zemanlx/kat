package reporter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/zemanlx/kat/internal/evaluator"
)

// OutputFormat specifies the output format for test results.
type OutputFormat int

const (
	// FormatDefault outputs summary only (like go test without -v).
	FormatDefault OutputFormat = iota
	// FormatVerbose outputs detailed test results (like go test -v).
	FormatVerbose
	// FormatJSON outputs JSON test events (like go test -json).
	FormatJSON
)

// Reporter handles formatting and reporting of test results.
type Reporter struct {
	out io.Writer

	format OutputFormat

	// Global stats
	totalTests  int
	passedTests int
	failedTests int

	startTime time.Time
}

var errTestsFailed = errors.New("tests failed")

// New creates a new Reporter that writes to the given output.
func New(out io.Writer) *Reporter {
	return &Reporter{
		out:       out,
		format:    FormatDefault,
		startTime: time.Now(),
	}
}

// SetFormat sets the output format for the reporter.
func (r *Reporter) SetFormat(format OutputFormat) {
	r.format = format
}

// TestEvent represents a JSON test event (similar to go test -json).
type TestEvent struct {
	Time    time.Time `json:"time"`
	Action  string    `json:"action"`
	Package string    `json:"package,omitempty"`
	Test    string    `json:"test,omitempty"`
	Elapsed float64   `json:"elapsed,omitempty"`
	Output  string    `json:"output,omitempty"`
}

// emitJSON writes a JSON test event.
func (r *Reporter) emitJSON(event TestEvent) {
	event.Time = time.Now()
	// Use json.Encoder to safely encode (and defaults to HTML escaping,
	// though not strictly required for CLI logs, it's safer).
	// It automatically adds a newline.
	if err := json.NewEncoder(r.out).Encode(event); err != nil {
		fmt.Fprintf(r.out, "{\"Action\":\"error\",\"Test\":\"%s\",\"Package\":\"%s\",\"Output\":\"json error: %v\"}\n", event.Test, event.Package, err)
	}
}

// SuiteReporter handles reporting for a specific test suite.
type SuiteReporter struct {
	rep  *Reporter
	name string

	startTime   time.Time
	passedTests int
	failedTests int

	// testStart tracks the start time of the current test.
	// Only valid during a test execution.
	testStart time.Time

	firstFailure bool // Track if this is first failure in non-verbose mode
}

// StartSuite reports the start of a test suite.
func (r *Reporter) StartSuite(suiteName string) *SuiteReporter {
	sr := &SuiteReporter{
		rep:          r,
		name:         suiteName,
		startTime:    time.Now(),
		firstFailure: true,
	}

	switch r.format {
	case FormatVerbose:
		fmt.Fprintf(r.out, "\n=== RUN   %s\n", suiteName)
	case FormatJSON:
		r.emitJSON(TestEvent{
			Action:  "run",
			Package: suiteName,
		})
	case FormatDefault:
		// Default format doesn't output suite start
		break
	}

	return sr
}

// StartTest reports the start of an individual test.
func (s *SuiteReporter) StartTest(testName string) {
	s.rep.totalTests++
	s.testStart = time.Now()

	switch s.rep.format {
	case FormatVerbose:
		fmt.Fprintf(s.rep.out, "=== RUN   %s/%s\n", s.name, testName)
	case FormatJSON:
		s.rep.emitJSON(TestEvent{
			Action:  "run",
			Package: s.name,
			Test:    testName,
		})
	case FormatDefault:
		// Default format doesn't output test start
		break
	}
}

// ReportPass reports a passing test.
func (s *SuiteReporter) ReportPass(testName string) {
	s.rep.passedTests++
	s.passedTests++
	elapsed := time.Since(s.testStart).Seconds()

	switch s.rep.format {
	case FormatVerbose:
		fmt.Fprintf(s.rep.out, "--- PASS: %s/%s (%.2fs)\n", s.name, testName, elapsed)
	case FormatJSON:
		s.rep.emitJSON(TestEvent{
			Action:  "pass",
			Package: s.name,
			Test:    testName,
			Elapsed: elapsed,
		})
	case FormatDefault:
		// Default format doesn't output individual test passes
		break
	}
}

// ReportFail reports a failing test with a message.
func (s *SuiteReporter) ReportFail(testName, message string) {
	s.rep.failedTests++
	s.failedTests++
	elapsed := time.Since(s.testStart).Seconds()

	// Trim trailing whitespace to prevent extra empty lines in output
	message = strings.TrimRightFunc(message, unicode.IsSpace)

	switch s.rep.format {
	case FormatVerbose:
		fmt.Fprintf(s.rep.out, "--- FAIL: %s/%s (%.2fs)\n", s.name, testName, elapsed)
		s.printIndented(message)
	case FormatJSON:
		s.rep.emitJSON(TestEvent{
			Action:  "output",
			Package: s.name,
			Test:    testName,
			Output:  message + "\n",
		})
		s.rep.emitJSON(TestEvent{
			Action:  "fail",
			Package: s.name,
			Test:    testName,
			Elapsed: elapsed,
		})
	case FormatDefault:
		// Only show failures in default mode
		if s.firstFailure {
			s.firstFailure = false
			fmt.Fprintf(s.rep.out, "\n")
		}

		fmt.Fprintf(s.rep.out, "--- FAIL: %s/%s (%.2fs)\n", s.name, testName, elapsed)
		s.printIndented(message)
	}
}

func (s *SuiteReporter) printIndented(message string) {
	lines := strings.Split(message, "\n")
	for _, line := range lines {
		if line == "" {
			fmt.Fprintln(s.rep.out)
		} else {
			fmt.Fprintf(s.rep.out, "    %s\n", line)
		}
	}
}

// ReportResult reports a test result from the evaluator.
func (s *SuiteReporter) ReportResult(testName string, result *evaluator.TestResult) {
	if result.Passed {
		s.ReportPass(testName)
	} else {
		s.ReportFail(testName, result.Message)
	}
}

// End reports the end of a test suite.
func (s *SuiteReporter) End() {
	elapsed := time.Since(s.startTime).Seconds()

	switch s.rep.format {
	case FormatDefault:
		// In non-verbose mode, print ok/FAIL line for each suite
		if s.failedTests > 0 {
			fmt.Fprintf(s.rep.out, "FAIL\t%s\t%.3fs\n", s.name, elapsed)
		} else {
			fmt.Fprintf(s.rep.out, "ok  \t%s\t%.3fs\n", s.name, elapsed)
		}
	case FormatJSON:
		// JSON mode emits package-level result
		if s.failedTests > 0 {
			s.rep.emitJSON(TestEvent{
				Action:  "fail",
				Package: s.name,
				Elapsed: elapsed,
			})
		} else {
			s.rep.emitJSON(TestEvent{
				Action:  "pass",
				Package: s.name,
				Elapsed: elapsed,
			})
		}
	case FormatVerbose:
		// Verbose mode doesn't output suite-level lines
		break
	}
}

// Summary prints the final test summary and returns an error if tests failed.
func (r *Reporter) Summary() error {
	elapsed := time.Since(r.startTime).Seconds()

	switch r.format {
	case FormatJSON:
		// Overall result
		if r.failedTests > 0 {
			r.emitJSON(TestEvent{
				Action:  "fail",
				Elapsed: elapsed,
			})
		} else {
			r.emitJSON(TestEvent{
				Action:  "pass",
				Elapsed: elapsed,
			})
		}
	case FormatVerbose:
		// Summary only in default and verbose modes
		if r.failedTests > 0 {
			fmt.Fprintf(r.out, "FAIL\n")
		} else {
			fmt.Fprintf(r.out, "PASS\n")
		}
	case FormatDefault:
		break
	}

	if r.failedTests > 0 {
		return fmt.Errorf("%w: %d", errTestsFailed, r.failedTests)
	}

	return nil
}

// Stats returns the current test statistics.
func (r *Reporter) Stats() (total, passed, failed int) {
	return r.totalTests, r.passedTests, r.failedTests
}
