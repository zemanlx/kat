package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	admissionregv1 "k8s.io/api/admissionregistration/v1"
	admissionv1beta1 "k8s.io/api/admissionregistration/v1beta1"

	"github.com/zemanlx/kat/internal/evaluator"
	"github.com/zemanlx/kat/internal/loader"
	"github.com/zemanlx/kat/internal/reporter"
)

func main() {
	if err := run(context.Background(), os.Args, os.Getenv, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run is testable: inject args/getenv/stdin/stdout.
func run(_ context.Context, args []string, _ func(string) string, _ *os.File, stdout *os.File) error {
	cfg, err := parseFlags(args, stdout)
	if err != nil {
		return err
	}

	suites, err := loadSuites(cfg.testPaths, cfg.runPattern)
	if err != nil {
		return err
	}

	return executeTests(suites, cfg, stdout)
}

type config struct {
	runPattern string
	verbose    bool
	jsonOutput bool
	testPaths  []string
}

func parseFlags(args []string, stdout *os.File) (*config, error) {
	fs := flag.NewFlagSet(args[0], flag.ExitOnError)
	fs.SetOutput(stdout)

	runPattern := fs.String("run", "", "run only tests matching pattern")
	verbose := fs.Bool("v", false, "verbose output")
	jsonOutput := fs.Bool("json", false, "output test results in JSON format")

	if err := fs.Parse(args[1:]); err != nil {
		return nil, fmt.Errorf("parse flags: %w", err)
	}

	testPaths := []string{"."}
	if fs.NArg() > 0 {
		testPaths = fs.Args()
	}

	return &config{
		runPattern: *runPattern,
		verbose:    *verbose,
		jsonOutput: *jsonOutput,
		testPaths:  testPaths,
	}, nil
}

func loadSuites(paths []string, pattern string) ([]*loader.TestSuite, error) {
	var suites []*loader.TestSuite

	for _, path := range paths {
		pathSuites, err := loader.Load(path, pattern)
		if err != nil {
			return nil, fmt.Errorf("load test suites from %s: %w", path, err)
		}

		suites = append(suites, pathSuites...)
	}

	return suites, nil
}

func executeTests(suites []*loader.TestSuite, cfg *config, stdout *os.File) error {
	eval, err := evaluator.New()
	if err != nil {
		return fmt.Errorf("create evaluator: %w", err)
	}

	rep := reporter.New(stdout)
	configureReporter(rep, cfg)

	for _, suite := range suites {
		if err := runSuite(eval, rep, suite); err != nil {
			return err
		}
	}

	if err := rep.Summary(); err != nil {
		return fmt.Errorf("test summary: %w", err)
	}

	return nil
}

func configureReporter(rep *reporter.Reporter, cfg *config) {
	switch {
	case cfg.jsonOutput:
		rep.SetFormat(reporter.FormatJSON)
	case cfg.verbose:
		rep.SetFormat(reporter.FormatVerbose)
	default:
		rep.SetFormat(reporter.FormatDefault)
	}
}

func runSuite(eval *evaluator.Evaluator, rep *reporter.Reporter, suite *loader.TestSuite) error {
	suiteRep := rep.StartSuite(suite.Name)
	defer suiteRep.End()

	for _, test := range suite.Tests {
		suiteRep.StartTest(test.Name)

		mutatingPolicy, validatingPolicy, validatingBinding := findPolicies(suite, test.PolicyName)

		if mutatingPolicy == nil && validatingPolicy == nil {
			suiteRep.ReportFail(test.Name, fmt.Sprintf("policy %q not found", test.PolicyName))

			continue
		}

		// Evaluate test
		result := eval.EvaluateTest(mutatingPolicy, validatingPolicy, validatingBinding, test)

		suiteRep.ReportResult(test.Name, result)
	}

	return nil
}

func findPolicies(suite *loader.TestSuite, policyName string) (*admissionv1beta1.MutatingAdmissionPolicy, *admissionregv1.ValidatingAdmissionPolicy, *admissionregv1.ValidatingAdmissionPolicyBinding) {
	var mutatingPolicy *admissionv1beta1.MutatingAdmissionPolicy

	var validatingPolicy *admissionregv1.ValidatingAdmissionPolicy

	var validatingBinding *admissionregv1.ValidatingAdmissionPolicyBinding

	for _, policy := range suite.MutatingPolicies {
		if policy.Name == policyName {
			mutatingPolicy = policy

			break
		}
	}

	if mutatingPolicy == nil {
		for _, policy := range suite.ValidatingPolicies {
			if policy.Name == policyName {
				validatingPolicy = policy
				// Find matching binding
				for _, binding := range suite.ValidatingBindings {
					if binding.Spec.PolicyName == policy.Name {
						validatingBinding = binding

						break
					}
				}

				break
			}
		}
	}

	return mutatingPolicy, validatingPolicy, validatingBinding
}
