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

	totalTests  int
	passedTests int
	failedTests int

	startTime time.Time
	testStart time.Time // Track individual test start time

	// Suite-level tracking
	currentSuite      string
	suiteStartTime    time.Time
	suitePassedTests  int
	suiteFailedTests  int
	suiteFirstFailure bool // Track if this is first failure in non-verbose mode
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

	data, err := json.Marshal(event)
	if err != nil {
		// Should never happen for our simple structs, but handle gracefully
		fmt.Fprintf(r.out, "{\"Action\":\"error\",\"Test\":\"%s\",\"Package\":\"%s\",\"Output\":\"%v\"}\n", event.Test, event.Package, err)

		return
	}

	fmt.Fprintf(r.out, "%s\n", data)
}

// StartSuite reports the start of a test suite.
func (r *Reporter) StartSuite(suiteName string) {
	// Report previous suite if exists
	if r.currentSuite != "" {
		r.endSuite()
	}

	r.currentSuite = suiteName
	r.suiteStartTime = time.Now()
	r.suitePassedTests = 0
	r.suiteFailedTests = 0
	r.suiteFirstFailure = true

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
}

// StartTest reports the start of an individual test.
func (r *Reporter) StartTest(suiteName, testName string) {
	r.totalTests++
	r.testStart = time.Now()

	switch r.format {
	case FormatVerbose:
		fmt.Fprintf(r.out, "=== RUN   %s/%s\n", suiteName, testName)
	case FormatJSON:
		r.emitJSON(TestEvent{
			Action:  "run",
			Package: suiteName,
			Test:    testName,
		})
	case FormatDefault:
		// Default format doesn't output test start
		break
	}
}

// ReportPass reports a passing test.
func (r *Reporter) ReportPass(suiteName, testName string) {
	r.passedTests++
	r.suitePassedTests++
	elapsed := time.Since(r.testStart).Seconds()

	switch r.format {
	case FormatVerbose:
		fmt.Fprintf(r.out, "--- PASS: %s/%s (%.2fs)\n", suiteName, testName, elapsed)
	case FormatJSON:
		r.emitJSON(TestEvent{
			Action:  "pass",
			Package: suiteName,
			Test:    testName,
			Elapsed: elapsed,
		})
	case FormatDefault:
		// Default format doesn't output individual test passes
		break
	}
}

// ReportFail reports a failing test with a message.
func (r *Reporter) ReportFail(suiteName, testName, message string) {
	r.failedTests++
	r.suiteFailedTests++
	elapsed := time.Since(r.testStart).Seconds()

	// Trim trailing whitespace to prevent extra empty lines in output
	message = strings.TrimRightFunc(message, unicode.IsSpace)

	switch r.format {
	case FormatVerbose:
		fmt.Fprintf(r.out, "--- FAIL: %s/%s (%.2fs)\n", suiteName, testName, elapsed)
		r.printIndented(message)
	case FormatJSON:
		r.emitJSON(TestEvent{
			Action:  "output",
			Package: suiteName,
			Test:    testName,
			Output:  message + "\n",
		})
		r.emitJSON(TestEvent{
			Action:  "fail",
			Package: suiteName,
			Test:    testName,
			Elapsed: elapsed,
		})
	case FormatDefault:
		// Only show failures in default mode
		if r.suiteFirstFailure {
			r.suiteFirstFailure = false
			fmt.Fprintf(r.out, "\n")
		}

		fmt.Fprintf(r.out, "--- FAIL: %s/%s (%.2fs)\n", suiteName, testName, elapsed)
		r.printIndented(message)
	}
}

func (r *Reporter) printIndented(message string) {
	lines := strings.Split(message, "\n")
	for _, line := range lines {
		if line == "" {
			fmt.Fprintln(r.out)
		} else {
			fmt.Fprintf(r.out, "    %s\n", line)
		}
	}
}

// ReportResult reports a test result from the evaluator.
func (r *Reporter) ReportResult(suiteName, testName string, result *evaluator.TestResult) {
	if result.Passed {
		r.ReportPass(suiteName, testName)
	} else {
		r.ReportFail(suiteName, testName, result.Message)
	}
}

// endSuite reports the end of a test suite.
func (r *Reporter) endSuite() {
	if r.currentSuite == "" {
		return
	}

	elapsed := time.Since(r.suiteStartTime).Seconds()

	switch r.format {
	case FormatDefault:
		// In non-verbose mode, print ok/FAIL line for each suite
		if r.suiteFailedTests > 0 {
			fmt.Fprintf(r.out, "FAIL\t%s\t%.3fs\n", r.currentSuite, elapsed)
		} else {
			fmt.Fprintf(r.out, "ok  \t%s\t%.3fs\n", r.currentSuite, elapsed)
		}
	case FormatJSON:
		// JSON mode emits package-level result
		if r.suiteFailedTests > 0 {
			r.emitJSON(TestEvent{
				Action:  "fail",
				Package: r.currentSuite,
				Elapsed: elapsed,
			})
		} else {
			r.emitJSON(TestEvent{
				Action:  "pass",
				Package: r.currentSuite,
				Elapsed: elapsed,
			})
		}
	case FormatVerbose:
		// Verbose mode doesn't output suite-level lines
		break
	}

	r.currentSuite = ""
}

// Summary prints the final test summary and returns an error if tests failed.
func (r *Reporter) Summary() error {
	// End the last suite if there is one
	if r.currentSuite != "" {
		r.endSuite()
	}

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
		// Summary only in default and verbose modes (after suite lines)
		if r.failedTests > 0 {
			fmt.Fprintf(r.out, "FAIL\n")
		} else {
			fmt.Fprintf(r.out, "PASS\n")
		}
	case FormatDefault:
		// Default mode summary handled above/inline
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
