# kat - Kubernetes Admission Tester

`kat` is a lightweight, local testing tool for Kubernetes Admission Policies (ValidatingAdmissionPolicy and MutatingAdmissionPolicy). It allows you to write test cases using standard Kubernetes manifests and verify your policies' behavior without needing a running cluster.

## Features

- **Standard Kubernetes YAML**: Write tests using plain K8s manifests - no new DSL to learn.
- **Full CEL Support**: Uses the official Kubernetes CEL libraries for 100% accurate evaluation.
- **Comprehensive Policy Support**:
  - `ValidatingAdmissionPolicy` (Allow, Deny, Warn, Audit)
  - `MutatingAdmissionPolicy` (Mutate, No-op)
- **All Operations**: Supports CREATE, UPDATE, DELETE, and CONNECT operations.
- **Golden File Testing**: Automatically verifies mutated objects against expected golden files.
- **Rich Context**: Simulate complex scenarios with `userInfo`, `namespaceObject`, and `matchConditions`.
- **Parameter Testing**: Test parameter-driven policies (`paramKind`/`paramRef`).

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

The recommended way to use `kat` is to run it from the root of your repository. It will automatically discover and execute all tests found in `tests/` directories recursively.

```bash
kat .
```

This commands will:

1. Find all `tests` directories.
2. Locate the corresponding Policy and Binding for each test (by looking up in the directory tree).
3. Execute all found tests.

You can also target specific directories or files:

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

This allows you to keep your tests co-located with your policy definitions. You just need to add a `tests/` folder alongside your manifests.

**Example Layout:**

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

In this setup, running `kat .` at the root will automatically find the `tests` directory, associate it with the policy in the parent `team-label-policy` directory, and execute the tests.

## Writing Tests

Tests are defined by file naming conventions. The filename structure determines the test type and expectations.

### File Naming Convention

Pattern: `<policy-name>.<test-name>.<expect>.<type>.yaml`

**Requirement:** The `<policy-name>` prefix must match the `metadata.name` of the policy being tested.
*(If a directory contains only a single policy, kats automatically associates all tests with that policy).*

- **expect**: `allow`, `deny`, `warn`, `audit` (for Validating)
- **type**: `object`, `oldObject`, `request`, `params`

### Validating Admission Policy

**1. Expect Allow:**
Create a file ending in `.allow.object.yaml`.

```yaml
# my-policy.test-1.allow.object.yaml
apiVersion: v1
kind: Pod
metadata:
  name: allowed-pod
  labels:
    cost-center: "123"
```

**2. Expect Deny:**
Create a file ending in `.deny.object.yaml`.

```yaml
# my-policy.test-2.deny.object.yaml
apiVersion: v1
kind: Pod
metadata:
  name: denied-pod
  # Missing required labels
```

**3. Expect Specific Message:**
Add a `.message.txt` file side-by-side.

```text
# my-policy.test-2.deny.message.txt
Pod must have a cost-center label
```

### Mutating Admission Policy

**1. Mutation Test:**
Provide the input object and the expected output (golden file).

- Input: `my-policy.test-1.object.yaml`
- Expected: `my-policy.test-1.gold.yaml`

If the actual mutation result differs from the golden file, the test fails and prints a diff.

### Advanced Scenarios

#### Request Context (`.request.yaml`)

Use a `.request.yaml` file to provide additional admission context like user info or namespace details.

```yaml
# my-policy.test-1.allow.request.yaml
operation: CREATE
userInfo:
  username: "system:serviceaccount:kube-system:job-controller"
namespaceObject:
  metadata:
    labels:
      environment: production
```

#### Parameters (`.params.yaml`)

For policies using `paramKind`, provide the parameter resource.

```yaml
# my-policy.test-1.allow.params.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: policy-config
data:
  excludedNamespaces: "kube-system,monitoring"
```

#### Operations (UPDATE / DELETE)

- **UPDATE**: Provide both `.object.yaml` (new) and `.oldObject.yaml` (old).
- **DELETE**: Provide only `.oldObject.yaml` (resource being deleted).

## Examples

Check the [test-policies-pass](./test-policies-pass/) directory for a
comprehensive set of examples covering:

- Basic validation and mutation
- Parameters and ConfigMaps
- Namespace-based logic
- `CONNECT` operations (kubectl exec)
- Match conditions
- Warnings and Audit annotations
