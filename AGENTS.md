# AGENTS.md

Orientation for AI coding agents working on **kat**, a local tester for Kubernetes
Admission Policies (`ValidatingAdmissionPolicy` and `MutatingAdmissionPolicy`).

## Project overview

- Language: Go (module `github.com/zemanlx/kat`, `go 1.27.1`).
- Entry point: `main.go`. Core logic under `internal/`:
  - `internal/loader` — discovers suites, parses policies/bindings and test files.
  - `internal/evaluator` — runs CEL evaluation and checks expected outcomes.
  - `internal/reporter` — renders default / verbose / JSON output.
- `kat` runs entirely offline; no cluster required. It mirrors `go test` semantics
  (`-run`, `-v`, `-json`, exit `0` on pass, non-zero on any failure).

## Build, test, lint

```bash
go build ./...            # build everything
go test ./...             # run all Go tests
go test -v ./...          # exactly what CI runs (.github/workflows/ci.yml)
go test -update ./...     # regenerate golden files (testdata/*.golden)
golangci-lint run         # lint (golangci-lint v2; config: .golangci.yaml)
```

- CI (`.github/workflows/ci.yml`) runs `go test -v ./...`, `golangci-lint-action@v7`
  (in the root and in `conformance/`), and the conformance matrix (see
  [Conformance tests](#conformance-tests)).
- Go golden files (e.g. `testdata/json_output.golden`) are written by the custom
  `-update` flag in `main_test.go`. After changing reporter output, run
  `go test -update ./...`, then review the diff before committing.

## Running kat

```bash
go build -o kat . && ./kat ./test-policies-pass/validating/require-owner-label
./kat .                                  # discover & run every suite from cwd
./kat -v <dir>                           # verbose
./kat -json <dir>                        # newline-delimited go-test-json events
./kat -run "<regex>" <dir>               # filter test cases by name
```

- **`kat` takes directory paths, not single files** (`loader.Load` uses `os.ReadDir`).
  To run one case, use `-run`, not a file path.
- The JSON `"package"` field is the **suite directory basename**
  (`filepath.Base(dir)`), not the policy's `metadata.name`.

## How discovery works

1. `kat` recursively finds directories containing policy files.
2. For each such directory it loads policies/bindings, then reads the adjacent
   `tests/` subdirectory for test cases.
3. Policy files: `policy.yaml`/`policies.yaml`/`*.policy.yaml` (+ `.yml`).
   Binding files: `binding.yaml`/`bindings.yaml`/`*.binding.yaml` (+ `.yml`).
   Multiple resources may share one file, separated by `---`.
- Only `v1` policies are supported; `v1beta1` documents are a hard error.

## Test file conventions (the API is the filename)

Pattern: `<policy-name>.<test-name>.<expect>.<type>.yaml`

- `<policy-name>` must prefix a policy's `metadata.name`. With a single policy in
  the directory the prefix is optional (auto-associated).
- `<expect>`: `allow` | `deny` | `warn` | `audit`. Parsed by substring:
  `.deny.`/`.deny` ⇒ expect denied; everything else ⇒ expect allowed. This token
  is a **validating** concept: mutating policies allow (unless their params are
  missing under `parameterNotFoundAction: Deny`, or a match condition fails to
  evaluate), so omit it and name the case after what it mutates (assert the result
  via `.gold.yaml`).
- `<type>` (input suffixes): `.request.yaml`, `.object.yaml`, `.oldObject.yaml`,
  `.namespaceObject.yaml`, `.params.yaml`, `.annotations.yaml`, `.warnings.txt`,
  `.authorizer.yaml`. Files sharing a base name are merged into one case.
- Companion assertion files (matched by base name, not in the input list):
  `.gold.yaml` (expected mutated object), `.message.txt` (expected deny message).

Operation is inferred from presence: `object` only ⇒ CREATE; `oldObject` only ⇒
DELETE; both ⇒ UPDATE; set `operation:` in `.request.yaml` for CONNECT. An explicit
`operation:` that conflicts with the inferred one is an error.

The policy's `matchConstraints` and the binding's `matchResources` are applied the
same way the API server applies them (`internal/evaluator/match.go`). A request that
doesn't match them is allowed with no mutation. The request resource comes from the
object's kind via apimachinery's plural guess, which is right for every built-in
kind. CONNECT tests, and CRDs with irregular plurals, set `resource:` in
`.request.yaml`.

## Authoring tests

Use the **`write-kat-tests` skill** (`skills/write-kat-tests/SKILL.md`) whenever you
create or edit `kat` test cases. It contains the authoritative filename grammar
(`skills/write-kat-tests/reference/filename-grammar.md`) and copy-ready templates
(`skills/write-kat-tests/reference/templates/`). Always finish by running
`kat <dir>` and confirming exit code `0`.

## Conformance tests

`conformance/` is a separate Go module (`github.com/zemanlx/kat/conformance`, with
`replace github.com/zemanlx/kat => ../`) that checks kat against a real
kube-apiserver and etcd started by controller-runtime's envtest. The root
`go test ./...` never starts servers and the root `go.mod` does not depend on
controller-runtime.

```bash
./hack/conformance.sh                 # every version in conformance/k8s-versions.txt, in parallel
./hack/conformance.sh 1.37.x          # one version
GOTESTFLAGS="-v -run TestConformance/test-policies-pass/mutating" ./hack/conformance.sh 1.37.x
```

- The script installs a pinned `setup-envtest` into `bin/`, downloads the binaries
  into `bin/envtest`, and runs `go test ./...` in `conformance/` with
  `KUBEBUILDER_ASSETS` set. Running `go test` there without it fails fast.
  `CONFORMANCE_PARALLEL` (default 4) caps concurrent apiservers per version. On a
  host whose `/tmp` is mounted `noexec`, set `GOTMPDIR` to an executable directory.
- For every case in `test-policies-pass/` and `test-policies-fail/`, the harness
  sends the equivalent dry-run request (CONNECT: a real `pods/exec` POST) to the
  server and checks two layers:
  1. `TestConformance/<suite>/<case>`: kat's raw evaluation result, on the very
     inputs the server's CEL sees (server-defaulted object, stored `oldObject`,
     the server's `namespaceObject` and params, the impersonated identity,
     `request.dryRun`), must equal the server's decision, message, policy warnings,
     audit annotations and mutated object.
  2. `.../<case>/binary`: the built kat binary must pass a suite generated from the
     server's results (inputs in `.request.yaml`, expectations in
     `.message.txt`/`.warnings.txt`/`.annotations.yaml`/`.gold.yaml`). Generated
     suites are kept in `conformance/.artifacts/<server version>/` (uploaded by CI on
     failure); reproduce with `./kat <dir>`.
- A server-side error that is not an admission decision (for example, the fixture
  object is invalid) makes the case fail as `inconclusive`. A fixture that kat
  itself cannot load is skipped. When kat decides the fixture as written
  differently than on the server-equivalent inputs, the case logs a note: the
  fixture does not describe what a real cluster would evaluate.
- Cases are packed into shards so that no two cases in a shard need conflicting
  server state (an object present vs absent, different namespace labels or params).
  Each shard gets its own apiserver; suites run in parallel.
- Layer-2 gaps, covered by layer 1: kat cannot assert "no warnings" or "no extra
  annotations", `.request.yaml` has no `dryRun`, and a CONNECT `.request.yaml`
  cannot carry the `PodExecOptions` object. Inputs omit `managedFields`, and
  `validation.policy.admission.k8s.io/validation_failure` is not compared.

**Known divergences.** When kat and the server differ and the fix is not a small kat
change, add an entry to `conformance/known-divergences.yaml` with `suite`, optional
`case`, optional `minVersion`/`maxVersion` (Kubernetes minors, inclusive), a `reason`
and ideally an `issue` link. A listed case must fail; once it matches, the test fails
with "unexpected pass" until the entry is removed. An entry that matches no case of a
suite that ran also fails.

**Supported versions.** Kubernetes 1.36 and later (the first release serving both VAP
and MAP as v1); a minor is dropped when it reaches upstream end of life. The harness
fails fast on an older server. When a minor is released or dropped, update both
`conformance/k8s-versions.txt` and the `conformance` job's `matrix.k8s` in
`.github/workflows/ci.yml`, and the support statement in `README.md`.

## Conventions & gotchas

- Match existing code and comment style; keep changes minimal and scoped.
- Assertions are exact-match: deny `.message.txt` equals the message (whitespace
  trimmed); `.warnings.txt` matches warnings by index; `.annotations.yaml` matches
  the listed keys exactly (extra actual keys ignored).
- A mutating policy that mutates the object **requires** a `.gold.yaml`, or the case
  fails with "policy mutated the object but no .gold.yaml file was provided".
- Defining the same field in both `.request.yaml` and a split file is an error.
- Do not commit built binaries (e.g. `kat`, `kat-bin`); they are gitignored/temporary.
- Never commit, push, or change dependencies without explicit maintainer approval.
