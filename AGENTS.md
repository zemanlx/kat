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

- CI (`.github/workflows/ci.yml`) runs `go test -v ./...` and `golangci-lint-action@v7`.
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
  is a **validating** concept: mutating policies always allow, so omit it and name
  the case after what it mutates (assert the result via `.gold.yaml`).
- `<type>` (input suffixes): `.request.yaml`, `.object.yaml`, `.oldObject.yaml`,
  `.namespaceObject.yaml`, `.params.yaml`, `.annotations.yaml`, `.warnings.txt`,
  `.authorizer.yaml`. Files sharing a base name are merged into one case.
- Companion assertion files (matched by base name, not in the input list):
  `.gold.yaml` (expected mutated object), `.message.txt` (expected deny message).

Operation is inferred from presence: `object` only ⇒ CREATE; `oldObject` only ⇒
DELETE; both ⇒ UPDATE; set `operation:` in `.request.yaml` for CONNECT. An explicit
`operation:` that conflicts with the inferred one is an error.

## Authoring tests

Use the **`write-kat-tests` skill** (`skills/write-kat-tests/SKILL.md`) whenever you
create or edit `kat` test cases. It contains the authoritative filename grammar
(`skills/write-kat-tests/reference/filename-grammar.md`) and copy-ready templates
(`skills/write-kat-tests/reference/templates/`). Always finish by running
`kat <dir>` and confirming exit code `0`.

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
