package conformance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/version"

	"github.com/zemanlx/kat/internal/evaluator"
	"github.com/zemanlx/kat/internal/loader"
)

// artifactsRoot keeps the generated binary suites, per server version, for
// reproducing a failure with `kat <dir>`. kat's discovery skips hidden
// directories, so `kat .` from the repository root ignores them.
const artifactsRoot = ".artifacts"

// katBinary is the kat binary built by TestMain.
var katBinary string //nolint:gochecknoglobals // Set once by TestMain.

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Fprintln(os.Stderr, "KUBEBUILDER_ASSETS is not set; run hack/conformance.sh, which downloads kube-apiserver and etcd")

		return 1
	}

	// Like go test itself, prefer GOTMPDIR: some systems mount /tmp noexec.
	dir, err := os.MkdirTemp(os.Getenv("GOTMPDIR"), "kat-conformance-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}
	defer os.RemoveAll(dir)

	katBinary = filepath.Join(dir, "kat")

	build := exec.CommandContext(context.Background(), "go", "build", "-o", katBinary, ".")
	build.Dir = ".."
	build.Stdout, build.Stderr = os.Stderr, os.Stderr

	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build kat:", err)

		return 1
	}

	return m.Run()
}

// shardRun is one shard's apiserver and the suite it runs.
type shardRun struct {
	suite *loader.TestSuite
	shard *shard
	srv   *server
	eval  *evaluator.Evaluator
	// k8sVersion is the server's major.minor, which kat is told to behave like.
	k8sVersion string
}

func TestConformance(t *testing.T) {
	t.Parallel()

	known, err := loadDivergences("known-divergences.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// One short-lived server checks the version floor before any suite starts
	// and provides the REST mapping used to plan shards.
	srv, stop := startServer(t)
	mapper, ver := srv.mapper, srv.version

	stop()
	t.Logf("kube-apiserver %s", ver)

	t.Cleanup(func() {
		for _, e := range known.unmatched(ver) {
			t.Errorf("known-divergences.yaml entry for %s %q matches no case; remove or fix it", e.Suite, e.Case)
		}
	})

	// The fixture trees compared with the server, relative to the repository root.
	for _, root := range []string{"test-policies-pass", "test-policies-fail"} {
		suites, err := loader.Load(filepath.Join("..", root), "")
		if err != nil {
			t.Fatalf("load %s: %v", root, err)
		}

		for _, suite := range suites {
			suiteID := strings.TrimPrefix(filepath.ToSlash(suite.Path), "../")

			t.Run(suiteID, func(t *testing.T) {
				t.Parallel()
				runSuite(t, suiteID, suite, mapper, ver, known)
			})
		}
	}
}

// artifactsDir is where the suite's generated binary suites are written; it
// is emptied first.
func artifactsDir(t *testing.T, suiteID string, ver *version.Version) string {
	t.Helper()

	dir, err := filepath.Abs(filepath.Join(artifactsRoot, ver.String(), suiteID))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	return dir
}

func runSuite(t *testing.T, suiteID string, suite *loader.TestSuite, mapper meta.RESTMapper, ver *version.Version, known *divergences) {
	t.Helper()

	k8sVersion := fmt.Sprintf("%d.%d", ver.Major(), ver.Minor())

	eval, err := evaluator.NewForVersion(k8sVersion)
	if err != nil {
		t.Fatal(err)
	}

	cases := newCases(suiteID, suite, mapper)
	entries := make(map[*katCase]*divergence, len(cases))

	for _, c := range cases {
		entries[c] = known.lookup(suiteID, c.name, ver)
	}

	artifacts := artifactsDir(t, suiteID, ver)

	for i, s := range shardCases(cases) {
		h := &shardRun{suite: suite, shard: s, eval: eval, k8sVersion: k8sVersion}
		h.run(t, filepath.Join(artifacts, fmt.Sprintf("shard-%d", i+1), filepath.Base(suite.Path)))
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { report(t, c, entries[c]) })
	}
}

// run executes one shard on its own apiserver: seed, prepare inputs and kat's
// results, install policies, wait for them, query the server, then run the
// kat binary on the generated suite.
func (h *shardRun) run(t *testing.T, binaryDir string) {
	t.Helper()

	ctx := t.Context()

	srv, stop := startServer(t)
	defer stop()

	h.srv = srv

	if err := h.seed(ctx); err != nil {
		t.Fatal(err)
	}

	h.prepare(ctx)
	h.installPolicies(ctx)

	if err := h.installProbe(ctx); err != nil {
		t.Fatal(err)
	}

	if err := h.waitProbe(ctx); err != nil {
		t.Fatal(err)
	}

	h.query(ctx)

	if err := h.writeBinarySuite(binaryDir); err != nil {
		t.Fatal(err)
	}

	h.runBinary(ctx, binaryDir)
}

// prepare computes every case's inputs and kat's result, before policies exist.
func (h *shardRun) prepare(ctx context.Context) {
	for _, c := range h.shard.cases {
		if c.isInconclusive() {
			continue
		}

		if err := h.prepareInputs(ctx, c); err != nil {
			c.inconclusivef("%v", err)

			continue
		}

		h.evaluateKat(ctx, c)
	}
}

// query sends every case to the server and compares the results.
func (h *shardRun) query(ctx context.Context) {
	for _, c := range h.shard.cases {
		if c.isInconclusive() {
			continue
		}

		if c.run.server.policyErr == "" {
			h.execute(ctx, c)
		}

		c.compare()
		h.checkRealism(ctx, c)
	}
}

// checkRealism notes when kat decides the fixture as written differently from
// the server-equivalent inputs: the fixture then does not describe what a
// real cluster would evaluate.
func (h *shardRun) checkRealism(ctx context.Context, c *katCase) {
	raw := h.evaluateRawFixture(ctx, c)
	normalized := c.run.kat
	normalized.Object = nil

	if diff := cmp.Diff(normalized, raw, cmpopts.EquateEmpty()); diff != "" {
		c.notef("the fixture as written is decided differently than on server-equivalent inputs "+
			"(-server-equivalent +as written):\n%s", diff)
	}
}

// report turns the case's results into test failures, or into expected
// failures when known-divergences.yaml lists the case.
func report(t *testing.T, c *katCase, entry *divergence) {
	t.Helper()

	for _, n := range c.run.notes {
		t.Log(n)
	}

	layer1 := c.layer1Failures()

	var layer2 []string
	if c.run.binaryFailure != "" {
		layer2 = []string{c.run.binaryFailure}
	}

	if entry != nil {
		if len(layer1)+len(layer2) == 0 {
			t.Errorf("unexpected pass: %s is listed in known-divergences.yaml (%s) but now matches the kube-apiserver; remove the entry",
				c.id(), entry.Reason)

			return
		}

		t.Logf("known divergence: %s %s", entry.Reason, entry.Issue)

		for _, f := range append(layer1, layer2...) {
			t.Log(f)
		}

		return
	}

	for _, f := range layer1 {
		t.Error(f)
	}

	if c.run.binaryRan {
		t.Run("binary", func(t *testing.T) {
			for _, f := range layer2 {
				t.Error(f)
			}
		})
	}
}
