package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// minVersion is the oldest supported Kubernetes minor: the first release that
// serves both ValidatingAdmissionPolicy and MutatingAdmissionPolicy as
// admissionregistration.k8s.io/v1.
const minVersion = "1.36"

const auditPolicy = `apiVersion: audit.k8s.io/v1
kind: Policy
rules:
- level: Metadata
`

var errUnsupportedServer = errors.New("unsupported kube-apiserver")

// server is one envtest kube-apiserver (with etcd) and the clients to reach it
// as the cluster admin.
type server struct {
	cfg      *rest.Config
	client   kubernetes.Interface
	dynamic  dynamic.Interface
	mapper   meta.RESTMapper
	version  *version.Version
	auditLog string
}

// startServer starts an apiserver with a Metadata-level audit log, so that
// policy audit annotations can be read back. The returned stop function must
// be called once the server is no longer needed; it is also registered as a
// cleanup of t, and calling it twice is harmless.
func startServer(t *testing.T) (*server, func()) {
	t.Helper()

	dir := t.TempDir()
	policyPath := filepath.Join(dir, "audit-policy.yaml")
	auditLog := filepath.Join(dir, "audit.log")

	if err := os.WriteFile(policyPath, []byte(auditPolicy), 0o600); err != nil {
		t.Fatalf("write audit policy: %v", err)
	}

	env := &envtest.Environment{}
	env.ControlPlane.GetAPIServer().Configure().
		Set("audit-policy-file", policyPath).
		Set("audit-log-path", auditLog).
		Set("audit-log-mode", "blocking")

	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest (is KUBEBUILDER_ASSETS set? run hack/conformance.sh): %v", err)
	}

	var once sync.Once

	stop := func() {
		once.Do(func() {
			if err := env.Stop(); err != nil {
				t.Logf("stop envtest: %v", err)
			}
		})
	}
	t.Cleanup(stop)

	srv, err := newServer(cfg, auditLog)
	if err != nil {
		stop()
		t.Fatal(err)
	}

	return srv, stop
}

func newServer(cfg *rest.Config, auditLog string) (*server, error) {
	cfg.QPS = 200
	cfg.Burst = 400

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	ver, err := checkServer(client.Discovery())
	if err != nil {
		return nil, err
	}

	groups, err := restmapper.GetAPIGroupResources(client.Discovery())
	if err != nil {
		return nil, fmt.Errorf("discover API resources: %w", err)
	}

	return &server{
		cfg:      cfg,
		client:   client,
		dynamic:  dyn,
		mapper:   restmapper.NewDiscoveryRESTMapper(groups),
		version:  ver,
		auditLog: auditLog,
	}, nil
}

// checkServer fails fast on a server older than minVersion or one that does
// not serve both admission policy APIs as v1.
func checkServer(disc discovery.DiscoveryInterface) (*version.Version, error) {
	info, err := disc.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("get server version: %w", err)
	}

	ver, err := version.ParseGeneric(info.GitVersion)
	if err != nil {
		return nil, fmt.Errorf("parse server version %q: %w", info.GitVersion, err)
	}

	if !ver.AtLeast(version.MustParseGeneric(minVersion)) {
		return nil, fmt.Errorf("%w: kube-apiserver %s is older than %s, the first release that serves "+
			"ValidatingAdmissionPolicy and MutatingAdmissionPolicy as v1", errUnsupportedServer, info.GitVersion, minVersion)
	}

	resources, err := disc.ServerResourcesForGroupVersion("admissionregistration.k8s.io/v1")
	if err != nil {
		return nil, fmt.Errorf("discover admissionregistration.k8s.io/v1: %w", err)
	}

	served := make([]string, 0, len(resources.APIResources))
	for _, r := range resources.APIResources {
		served = append(served, r.Name)
	}

	for _, want := range []string{
		"validatingadmissionpolicies", "validatingadmissionpolicybindings",
		"mutatingadmissionpolicies", "mutatingadmissionpolicybindings",
	} {
		if !slices.Contains(served, want) {
			return nil, fmt.Errorf("%w: kube-apiserver %s does not serve admissionregistration.k8s.io/v1 %s",
				errUnsupportedServer, info.GitVersion, want)
		}
	}

	return ver, nil
}

// auditEvent is the subset of an audit.k8s.io/v1 Event the harness reads.
type auditEvent struct {
	AuditID          string            `json:"auditID"` //nolint:tagliatelle // The audit.k8s.io/v1 field name.
	Stage            string            `json:"stage"`
	ImpersonatedUser *auditUser        `json:"impersonatedUser"`
	Annotations      map[string]string `json:"annotations"`
}

type auditUser struct {
	Username string              `json:"username"`
	UID      string              `json:"uid"`
	Groups   []string            `json:"groups"`
	Extra    map[string][]string `json:"extra"`
}

var errNoAuditEvent = errors.New("audit event not found")

// auditEvent returns the ResponseComplete audit event of a request. The log is
// written in blocking mode, but the file may still lag the response slightly.
func (s *server) auditEvent(ctx context.Context, auditID string) (*auditEvent, error) {
	const (
		timeout  = 10 * time.Second
		interval = 50 * time.Millisecond
	)

	deadline := time.Now().Add(timeout)

	for {
		event, err := s.findAuditEvent(auditID)
		if err != nil || event != nil {
			return event, err
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: %s", errNoAuditEvent, auditID)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for audit event %s: %w", auditID, ctx.Err())
		case <-time.After(interval):
		}
	}
}

func (s *server) findAuditEvent(auditID string) (*auditEvent, error) {
	data, err := os.ReadFile(s.auditLog)
	if err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}

	// The last line may still be being written; read it on the next poll.
	data = data[:bytes.LastIndexByte(data, '\n')+1]

	needle := []byte(`"auditID":"` + auditID + `"`)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<24)

	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, needle) {
			continue
		}

		var event auditEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode audit event: %w", err)
		}

		if event.Stage == "ResponseComplete" {
			return &event, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan audit log: %w", err)
	}

	return nil, nil //nolint:nilnil // Not written yet; the caller polls.
}
