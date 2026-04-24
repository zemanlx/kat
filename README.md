<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/kat-logo-dark.svg">
    <img src="assets/kat-logo.svg" width="160" alt="Kat logo">
  </picture>
</p>

# kat - Kubernetes Admission Tester

[![CI](https://github.com/zemanlx/kat/actions/workflows/ci.yml/badge.svg)](https://github.com/zemanlx/kat/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/zemanlx/kat)](https://goreportcard.com/report/github.com/zemanlx/kat)
[![License](https://img.shields.io/github/license/zemanlx/kat)](https://github.com/zemanlx/kat/blob/main/LICENSE)

`kat` is a lightweight, local testing tool for Kubernetes Admission Policies (ValidatingAdmissionPolicy and MutatingAdmissionPolicy). It allows you to write test cases using standard Kubernetes manifests and verify your policies' behavior without needing a running cluster.

## Quick Start

Given a policy like this:

```text
my-policy/
├── policy.yaml
└── tests/
    ├── my-policy.good-pod.allow.object.yaml
    └── my-policy.bad-pod.deny.object.yaml
```

The simplest test is just a Kubernetes object:

```yaml
# tests/my-policy.good-pod.allow.object.yaml
apiVersion: v1
kind: Pod
metadata:
  name: good-pod
  labels:
    owner: platform-team
```

```yaml
# tests/my-policy.bad-pod.deny.object.yaml
apiVersion: v1
kind: Pod
metadata:
  name: bad-pod
  # Missing required label
```

Run it:

```bash
kat .
```

That's it. `kat` discovers the policy, finds the tests, and evaluates them. The filename tells `kat` everything: which policy to test (`my-policy`), what to expect (`allow` or `deny`), and what the file contains (`object`).

## Installation

```bash
go install github.com/zemanlx/kat@latest
```

Or build from source:

```bash
git clone https://github.com/zemanlx/kat.git
cd kat
go install
```

## Usage

Run from the root of your repository — `kat` will automatically discover and execute all tests found in `tests/` directories recursively:

```bash
kat .
```

Target specific directories or files:

```bash
# Run tests for a specific policy
kat ./policies/my-policy/tests

# Run a specific test case
kat ./policies/my-policy/tests/my-policy.basic-test.object.yaml
```

### Flags

- `-run <regex>`: Run only tests matching the regex pattern.
- `-v`: Verbose output (shows detailed execution steps).
- `-json`: Output results in JSON format.

```bash
kat -v -run "prod-.*-deny" .
```

## Project Structure & Discovery

`kat` is designed to fit naturally into existing Kubernetes repositories, including those using Kustomize.

The tool works by discovery:

1. It looks for `tests/` directories containing test files.
2. It looks for policy and binding files in the parent directory of `tests/`.

Supported filenames include:

- `policy.yaml` / `policies.yaml`
- `binding.yaml` / `bindings.yaml`
- Any file ending in `.policy.yaml` or `.binding.yaml`

**Note:** You can define multiple policies and bindings in a single file (separated by `---`), or split them across multiple files. The tool loads all valid policy/binding resources found in the directory.

```text
policies/
├── team-label-policy/
│   ├── kustomization.yaml  # (Optional) Kustomize file
│   ├── policy.yaml         # The AdmissionPolicy definition
│   ├── binding.yaml        # The AdmissionPolicyBinding
│   └── tests/              # Add this folder for kat
│       ├── team-label.has-label.allow.object.yaml
│       ├── team-label.missing.deny.object.yaml
│       └── ...
```

Running `kat .` at the root will automatically find the `tests` directory, associate it with the policy in the parent directory, and execute the tests.

## Writing Tests

### File Naming Convention

Pattern: `<policy-name>.<test-name>.<expect>.<type>.yaml`

The `<policy-name>` prefix must match the `metadata.name` of the policy being tested. If a directory contains only a single policy, `kat` automatically associates all tests with that policy.

| Part | Values | Description |
|------|--------|-------------|
| **expect** | `allow`, `deny`, `warn`, `audit` | Expected admission outcome |
| **type** | `object`, `oldObject`, `request`, `params`, `namespaceObject`, `authorizer`, `annotations`, `warnings` | What the file contains |

Multiple files with the same `<policy-name>.<test-name>.<expect>` prefix are merged into a single test case.

### Two Ways to Write Tests

You can provide test inputs as **a single `.request.yaml`** file or as **separate files per field** — or a mix of both.

#### All-in-One: `.request.yaml`

A `.request.yaml` file can contain the object, params, namespace context, and user info all in one place. This is the simplest way to write tests that need more than just an object.

```yaml
# my-policy.dev-deploy.allow.request.yaml
operation: CREATE
namespace: development
object:
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: dev-deployment
    namespace: development
  spec:
    replicas: 1
    selector:
      matchLabels:
        app: test
    template:
      metadata:
        labels:
          app: test
      spec:
        containers:
        - name: nginx
          image: nginx
namespaceObject:
  apiVersion: v1
  kind: Namespace
  metadata:
    name: development
    labels:
      environment: dev
params:
  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: policy-config
  data:
    maxReplicas: "10"
userInfo:
  username: "developer@example.com"
```

Available fields in `.request.yaml`:

| Field | Description |
|-------|-------------|
| `operation` | `CREATE`, `UPDATE`, `DELETE`, or `CONNECT` |
| `object` | The object being admitted |
| `oldObject` | Previous version (for UPDATE/DELETE) |
| `params` | Parameter resource (`paramKind`/`paramRef`) |
| `namespaceObject` | Namespace context with labels/annotations |
| `userInfo` | User making the request |
| `namespace` | Shorthand for request namespace |
| `name` | Shorthand for request name |
| `subResource` | Sub-resource being accessed (e.g., `status`) |
| `options` | Additional options for the request |

#### Split Files

For simpler tests, you can use separate files — each containing just one piece:

```text
my-policy.deploy-test.allow.object.yaml      # The object being admitted
my-policy.deploy-test.allow.params.yaml      # Policy parameters
my-policy.deploy-test.allow.request.yaml     # Additional context (userInfo, namespace, etc.)
```

This keeps individual files small and readable. All files sharing the same base name (`my-policy.deploy-test.allow`) are merged into one test case.

**Conflict detection:** If the same field (e.g., `object`) is defined in both `.request.yaml` and a separate `.object.yaml`, `kat` reports an error.

### Expected Outcomes

**Allow** — the object is admitted:
```yaml
# my-policy.good-pod.allow.object.yaml
```

**Deny** — the request is rejected. Add a `.message.txt` to verify the exact error:
```text
# my-policy.bad-pod.deny.message.txt
All workloads must have an 'owner' label
```

**Warn** — admitted with warnings. Add a `.warnings.txt`:
```text
# my-policy.old-api.warn.warnings.txt
Deprecated API version, migrate to apps/v1
```

**Audit** — admitted with audit annotations. Add an `.annotations.yaml`:
```yaml
# my-policy.flagged.audit.annotations.yaml
audit-annotation-key: "violation detected"
```

### Mutating Policies

For mutating policies, provide a **golden file** (`.gold.yaml`) with the expected output:

```text
my-policy.add-labels.object.yaml    # Input object
my-policy.add-labels.gold.yaml      # Expected object after mutation
```

If the actual mutation result differs from the golden file, the test fails with a diff. This also works with all-in-one `.request.yaml` files — just place a `.gold.yaml` alongside it.

### Authorizer Mocking

Mock Kubernetes Authorizer responses (SubjectAccessReview) for policies using `authorizer` in CEL:

```yaml
# my-policy.check-perms.deny.authorizer.yaml
- group: ""
  resource: "pods"
  namespace: "default"
  verb: "create"
  decision: "allow"
```

Any check not explicitly mocked returns "NoOpinion".

### Operations

- **CREATE** (default): Provide `object` (via `.object.yaml` or in `.request.yaml`).
- **UPDATE**: Provide both `object` and `oldObject`. Operation is inferred automatically.
- **DELETE**: Provide only `oldObject`. Operation is inferred automatically.
- **CONNECT**: Set `operation: CONNECT` in `.request.yaml`.

Operation inference works the same way whether fields are in separate files or consolidated in `.request.yaml`. If you set `operation:` explicitly and it conflicts with what would be inferred from the fields present, `kat` reports an error.

## Features

- **Standard Kubernetes YAML** — no new DSL to learn
- **Full CEL Support** — uses official Kubernetes CEL libraries for 100% accurate evaluation
- **Comprehensive Policy Support** — `ValidatingAdmissionPolicy` and `MutatingAdmissionPolicy`
- **All Operations** — CREATE, UPDATE, DELETE, CONNECT
- **Golden File Testing** — verifies mutated objects against expected output
- **Rich Context** — `userInfo`, `namespaceObject`, `matchConditions`, authorizer mocking
- **Parameter Testing** — `paramKind`/`paramRef` with ConfigMaps or custom resources

## Examples

Check the [test-policies-pass](./test-policies-pass/) directory for a
comprehensive set of examples covering:

- Basic validation and mutation
- Parameters and ConfigMaps
- Namespace-based logic
- `CONNECT` operations (kubectl exec)
- Match conditions
- Warnings and Audit annotations
