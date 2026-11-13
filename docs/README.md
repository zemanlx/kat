# kat documentation

kat's documentation lives in the repository's top-level files. This page indexes it.

## Using kat

- [Quick Start](../README.md#quick-start): a minimal policy with an allow test and a deny test, and the command that runs them.
- [Installation](../README.md#installation): prebuilt binaries, `go install`, and building from source.
- [Usage](../README.md#usage): running kat on directories, and running one case with `-run`.
- [Flags](../README.md#flags): `-run`, `-v`, `-json` and `-k8s-version`.
- [Project Structure & Discovery](../README.md#project-structure--discovery): how kat finds policies, bindings and `tests/` directories.

## Writing tests

- [File Naming Convention](../README.md#file-naming-convention): the `<policy-name>.<test-name>.<expect>.<type>.yaml` pattern.
- [Two Ways to Write Tests](../README.md#two-ways-to-write-tests): a single `.request.yaml` file, or one file per field.
- [Expected Outcomes](../README.md#expected-outcomes): `allow`, `deny`, `warn` and `audit` cases, and their companion files.
- [Mutating Policies](../README.md#mutating-policies): asserting the mutated object with a `.gold.yaml` file.
- [Authorizer Mocking](../README.md#authorizer-mocking): mocking SubjectAccessReview answers for `authorizer` in CEL.
- [Operations](../README.md#operations): how CREATE, UPDATE, DELETE and CONNECT are inferred.
- [Write, Run, Validate](../README.md#write-run-validate): the loop for writing a test and checking it.
- [Gotchas & Constraints](../README.md#gotchas--constraints): cases where kat's behaviour is easy to misread.

## Conformance

- [Conformance with the API server](../README.md#conformance-with-the-api-server): the `conformance/` module, which checks kat against a real kube-apiserver, and `./hack/conformance.sh`.

## For AI agents

- [For AI agents](../README.md#for-ai-agents): machine-readable guidance for coding agents, with links to the agent instructions and the skill.
- [Using kat's skill in your own repo](../README.md#using-kats-skill-in-your-own-repo): how another repository points its coding agent at the skill.
- [AGENTS.md](../AGENTS.md): repository orientation for coding agents, covering commands, discovery and conventions.
- [write-kat-tests skill](../skills/write-kat-tests/SKILL.md): the authoritative guide to test filenames and templates, with [reference files](../skills/write-kat-tests/reference/) for the grammar and copy-ready templates.

## Features and examples

- [Features](../README.md#features): standard YAML input, CEL, both policy kinds, all operations, golden files, request context, and parameters.
- [Examples](../README.md#examples): the [test-policies-pass](../test-policies-pass/) directory, and the policy features it covers.

## Security and contributing

- [Security](../README.md#security): reporting a vulnerability, Scorecard, and verifying release attestations.
- [SECURITY.md](../SECURITY.md): how to report a vulnerability privately, kat's security design and how to verify releases.
- [CONTRIBUTING.md](../CONTRIBUTING.md): how to report bugs, propose changes, and the test, development and code style rules.
