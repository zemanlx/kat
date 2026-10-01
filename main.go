package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/zemanlx/kat/internal/evaluator"
	"github.com/zemanlx/kat/internal/loader"
	"github.com/zemanlx/kat/internal/reporter"
)

const defaultVersion = "(devel)"

var version = defaultVersion

type config struct {
	runPattern        string
	verbose           bool
	jsonOutput        bool
	version           bool
	kubernetesVersion string
	testPaths         []string
}

func main() {
	if err := run(context.Background(), os.Args, os.Getenv, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run is testable: inject args/getenv/stdin/stdout.
func run(ctx context.Context, args []string, _ func(string) string, _ *os.File, stdout *os.File) error {
	cfg, err := parseFlags(args, stdout)
	if err != nil {
		return err
	}

	if cfg.version {
		fmt.Fprintln(stdout, getVersion())

		return nil
	}

	suites, err := loadSuites(cfg.testPaths, cfg.runPattern)
	if err != nil {
		return err
	}

	return executeTests(ctx, suites, cfg, stdout)
}

func parseFlags(args []string, stdout *os.File) (*config, error) {
	fs := flag.NewFlagSet(args[0], flag.ExitOnError)
	fs.SetOutput(stdout)

	runPattern := fs.String("run", "", "run only tests matching pattern")
	verbose := fs.Bool("v", false, "verbose output")
	jsonOutput := fs.Bool("json", false, "output test results in JSON format")
	showVersion := fs.Bool("version", false, "print version and exit")
	kubernetesVersion := fs.String("k8s-version", evaluator.DefaultKubernetesVersion,
		"evaluate policies like the kube-apiserver of this major.minor version")

	if err := fs.Parse(args[1:]); err != nil {
		return nil, fmt.Errorf("parse flags: %w", err)
	}

	testPaths := []string{"."}
	if fs.NArg() > 0 {
		testPaths = fs.Args()
	}

	return &config{
		runPattern:        *runPattern,
		verbose:           *verbose,
		jsonOutput:        *jsonOutput,
		version:           *showVersion,
		kubernetesVersion: *kubernetesVersion,
		testPaths:         testPaths,
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

func executeTests(ctx context.Context, suites []*loader.TestSuite, cfg *config, stdout *os.File) error {
	eval, err := evaluator.NewForVersion(cfg.kubernetesVersion)
	if err != nil {
		return fmt.Errorf("create evaluator: %w", err)
	}

	rep := reporter.New(stdout)
	configureReporter(rep, cfg)

	for _, suite := range suites {
		if err := runSuite(ctx, eval, rep, suite); err != nil {
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

func runSuite(ctx context.Context, eval *evaluator.Evaluator, rep *reporter.Reporter, suite *loader.TestSuite) error {
	suiteRep := rep.StartSuite(suite.Name)
	defer suiteRep.End()

	for _, test := range suite.Tests {
		suiteRep.StartTest(test.Name)

		policies := suite.FindPolicies(test.PolicyName)
		if policies.Mutating == nil && policies.Validating == nil {
			suiteRep.ReportFail(test.Name, fmt.Sprintf("policy %q not found", test.PolicyName))

			continue
		}

		result := eval.EvaluateTest(ctx, policies, test)
		suiteRep.ReportResult(test.Name, result)
	}

	return nil
}

func getVersion() string {
	if version != defaultVersion {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}

	if info.Main.Version == "" || info.Main.Version == defaultVersion {
		return version
	}

	return info.Main.Version
}
