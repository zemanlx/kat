# Security policy

## Supported versions

Only the latest release is supported. Fixes ship in a new release. There are no backports.

## Reporting a vulnerability

Report it privately through [GitHub private vulnerability reporting](https://github.com/zemanlx/kat/security/advisories/new). Do not open a public issue.

Include:

- the affected version of kat (`kat -version` prints it)
- steps to reproduce, with the policies, bindings and test files involved
- the impact: what an attacker can read, write or bypass

kat is maintained by one person on a best-effort basis. I will acknowledge the report within 14 days and coordinate the fix and its disclosure through the advisory.

## Scope

kat reads the policy, binding and test files in the directories it is given. It writes nothing to disk and opens no network connection. Its report goes to stdout and its errors to stderr.

Reports that kat reads or writes data beyond those inputs, or opens a network connection, are in scope. A verdict that differs from the API server's is a bug: open a [GitHub issue](https://github.com/zemanlx/kat/issues).

## Security design

What kat can harm, if it fails or is compromised:

- **Verdicts.** Each test passes or fails, and `kat` exits non-zero when any test fails. A false pass, where a test passes but the API server would decide differently, lets a bad policy ship. A false fail blocks a good policy in CI.
- **kat as a library.** kar imports the `evaluator` package. It compiles policies with it and evaluates the objects it audits. A wrong verdict there changes which namespaces kar exempts and whether it writes a Deny binding.

Trust boundaries:

- The inputs are kat's flags and the policy, binding and test files in the directories it is given. kat reads no environment variables and no kubeconfig.
- As a library, the caller supplies the policy, binding and objects. kar takes them from the API server.
- Policy expressions are compiled and run by the CEL code of `k8s.io/apiserver`, the library the API server is built on.

How the design limits the harm:

- **Offline.** kat makes no network connection and holds no cluster credentials. `authorizer` checks are answered by a mock built from the test's `.authorizer.yaml` file, not by a cluster.
- **The server's code.** kat uses the CEL compiler, match conditions and patchers of `k8s.io/apiserver`, with its CEL cost limits: a per-call limit on each expression and a runtime budget per binding. Policy validation at creation is a port of the server's, so the conformance suite checks it against the server.
- **Conformance.** The conformance suite runs every case in `test-policies-pass` and `test-policies-fail` against a real kube-apiserver and etcd, started by envtest. For each Kubernetes version in `conformance/k8s-versions.json`, it compares kat's decision, message, warnings, audit annotations and mutated object with the server's. CI runs it on every push and pull request to `main`.
- **Small mechanism.** kat is one binary. It keeps no state between runs and implements no cryptography of its own.
- **Supply chain.** Go dependencies are pinned in `go.sum`, and CI actions are pinned by full commit SHA. Dependabot updates both. CodeQL (GitHub's default setup, so there is no workflow file) and golangci-lint, including gosec, run on every pull request. Releases are built in GitHub Actions and carry build provenance attestations from the next release on. [OpenSSF Scorecard](https://scorecard.dev/viewer/?uri=github.com/zemanlx/kat) checks the repository weekly.

## Verifying releases

Release archives carry GitHub build provenance attestations from the next release on. Verify a downloaded archive with:

```bash
gh attestation verify kat_linux_amd64.tar.gz --owner zemanlx
```

From the next release on, each GitHub release also attaches the provenance bundle as `kat_<version>.intoto.jsonl`, which covers every archive listed in `checksums.txt`. Verify an archive against it:

```bash
gh attestation verify kat_linux_amd64.tar.gz --bundle kat_<version>.intoto.jsonl --owner zemanlx
```
