# Input Format Specification

## Overview

This document defines the input format and UX for `kat` (Kubernetes Admission Tester).

## Tool Name

**kat** - Kubernetes Admission Tester

Following Go's testing conventions for familiarity and ease of use.

## Design Goals

1. **Familiar**: Use Kubernetes YAML conventions
2. **Simple**: Minimal boilerplate for basic tests
3. **Flexible**: Support complex scenarios when needed
4. **Self-contained**: Tests should be readable and understandable on their own

## Policy Files

Policy files contain pure Kubernetes resources that will be applied to the cluster:

```yaml
# test-policies-pass/sidecar-injection/policy.yaml
apiVersion: admissionregistration.k8s.io/v1beta1
kind: MutatingAdmissionPolicy
metadata:
  name: sidecar-injection
spec:
  # ... policy spec
```

These are the actual files used with `kubectl apply -f`.

## Input Format

### File Naming Convention

Tests use file naming patterns to define behavior. File suffixes map directly to AdmissionRequest fields.

**For MutatingAdmissionPolicy:**

- Input: `<policy-name>.<test-name>.object.yaml` - Resource to test (becomes `request.object`)
- Expected: `<policy-name>.<test-name>.gold.yaml` - Expected mutated output
- Context: `<policy-name>.<test-name>.request.yaml` - Optional request context

**For ValidatingAdmissionPolicy:**

- Allow: `<policy-name>.<test-name>.allow.object.yaml` - Should be allowed (becomes `request.object`)
- Deny: `<policy-name>.<test-name>.deny.object.yaml` - Should be denied (becomes `request.object`)
- Context: `<policy-name>.<test-name>.allow.request.yaml` or `.deny.request.yaml` - Optional request context

**For DELETE operations:**

- Resource: `<policy-name>.<test-name>.allow.oldObject.yaml` or `.deny.oldObject.yaml` - Resource being deleted (becomes `request.oldObject`)
- Context: `<policy-name>.<test-name>.allow.request.yaml` or `.deny.request.yaml` - Must contain `operation: DELETE`

**For UPDATE operations:**

- New state: `<policy-name>.<test-name>.allow.object.yaml` or `.deny.object.yaml` - New resource state (becomes `request.object`)
- Old state: `<policy-name>.<test-name>.allow.oldObject.yaml` or `.deny.oldObject.yaml` - Previous resource state (becomes `request.oldObject`)
- Context: `<policy-name>.<test-name>.allow.request.yaml` or `.deny.request.yaml` - Must contain `operation: UPDATE`

### Example Structure

```text
policies/
├── sidecar-injection/
│   ├── policy.yaml
│   ├── binding.yaml
│   └── tests/
│       ├── sidecar-injection.adding-istio-sidecar.object.yaml
│       ├── sidecar-injection.adding-istio-sidecar.gold.yaml
│       ├── sidecar-injection.skip-when-exists.object.yaml
│       └── sidecar-injection.skip-when-exists.gold.yaml
├── require-owner-label/
│   ├── policy.yaml
│   ├── binding.yaml
│   └── tests/
│       ├── require-owner-label.with-label.allow.object.yaml
│       └── require-owner-label.without-label.deny.object.yaml
├── block-team-ci-service-accounts/
│   ├── policy.yaml
│   ├── binding.yaml
│   └── tests/
│       ├── block-team-ci.allowed-core-infra.allow.object.yaml
│       ├── block-team-ci.allowed-core-infra.allow.request.yaml
│       ├── block-team-ci.blocked.deny.object.yaml
│       └── block-team-ci.blocked.deny.request.yaml
├── delete-protection/
│   ├── policy.yaml
│   ├── binding.yaml
│   └── tests/
│       ├── delete-protection.protected-resource.deny.oldObject.yaml
│       ├── delete-protection.protected-resource.deny.request.yaml
│       ├── delete-protection.unprotected-resource.allow.oldObject.yaml
│       └── delete-protection.unprotected-resource.allow.request.yaml
└── prevent-owner-change/
    ├── policy.yaml
    ├── binding.yaml
    └── tests/
        ├── prevent-owner-change.changing-owner.deny.object.yaml
        ├── prevent-owner-change.changing-owner.deny.oldObject.yaml
        ├── prevent-owner-change.changing-owner.deny.request.yaml
        ├── prevent-owner-change.same-owner.allow.object.yaml
        └── prevent-owner-change.same-owner.allow.oldObject.yaml
```

**Key principles**:

- **No special test manifests** - Just plain Kubernetes resources
- **File naming maps to AdmissionRequest** - `.object.yaml` → `request.object`, `.oldObject.yaml` → `request.oldObject`
- **Explicit and clear** - File suffix tells you exactly what it represents
- **Handles all operations** - CREATE, UPDATE, DELETE all follow the same pattern

## How It Works

### Operation Inference

If `.request.yaml` doesn't specify `operation`, it's inferred from files present:

- Only `.object.yaml` present → `operation: CREATE`
- Only `.oldObject.yaml` present → `operation: DELETE`
- Both `.object.yaml` and `.oldObject.yaml` present → `operation: UPDATE`

Note: `CONNECT` is not inferred. Provide a `.request.yaml` with `operation: CONNECT` and include `name`, `namespace`, `subResource` (e.g., `exec`), and appropriate `options` (e.g., PodExecOptions). `request.object` is null for CONNECT.

### For MutatingAdmissionPolicy

1. Tool finds pairs of files: `*.object.yaml` and `*.gold.yaml`
2. Loads `.object.yaml` as `request.object`
3. Loads optional `.request.yaml` for additional context (userInfo, namespace, etc.)
4. Applies policy to the request
5. Compares result to the `.gold.yaml` file
6. Reports diff if they don't match

### For ValidatingAdmissionPolicy

1. Tool finds `*.allow.object.yaml` files - expects policy to allow them
2. Tool finds `*.deny.object.yaml` files - expects policy to deny them
3. Loads `.object.yaml` or `.oldObject.yaml` as appropriate
4. Loads optional `.request.yaml` for additional context
5. Evaluates policy against the request
6. Reports error if behavior doesn't match expectation

## Request Context

For policies that use `request.*` fields (like `request.userInfo.username`, `request.operation`), provide a sidecar file with request context.

**Naming:** `<test-name>.request.yaml`

**Example for CREATE/UPDATE:** `block-team-ci.blocked.fail.request.yaml`

```yaml
operation: CREATE
userInfo:
  username: "system:serviceaccount:team-ci-random:default"
  groups: ["system:serviceaccounts", "system:authenticated"]
  uid: "12345"
  extra:
    some-key: ["value1", "value2"]
namespace: prod-namespace
namespaceObject:
  apiVersion: v1
  kind: Namespace
  metadata:
    name: prod-namespace
    labels:
      environment: production
      team: platform
dryRun: false
```

**Supported fields:**

- `operation` - CREATE, UPDATE, DELETE, CONNECT
- `userInfo.username` - string
- `userInfo.uid` - string
- `userInfo.groups` - array of strings
- `userInfo.extra` - map of string arrays (e.g., `{"some-key": ["value1", "value2"]}`)
- `namespace` - string (namespace name)
- `namespaceObject` - full Namespace resource (required if policy uses `namespaceObject.*`)
- `name` - string (resource name, usually inferred from test resource)
- `uid` - string (unique ID for admission call, auto-generated if not provided)
- `dryRun` - boolean (default: false)
- `oldObject` - full resource YAML (for UPDATE/DELETE operations)
- `options` - operation options (CreateOptions, UpdateOptions, DeleteOptions)
- `subResource` - string (e.g., "scale", "status")

**Important:**

- If the policy or binding references any `request.*` field, you **must** provide it in the `.request.yaml` file (unless it can be inferred from the test resource itself, like `object` or `name`)
- The tool will fail the test with a clear error if the policy references request fields that weren't provided
- Common fields that need explicit provision: `operation`, `userInfo`, `namespace`, `namespaceObject`, `oldObject`, `dryRun`

**How it works:**

1. Tool loads `.object.yaml` and/or `.oldObject.yaml` files
2. Tool loads optional `.request.yaml` file for additional context
3. Builds `AdmissionRequest`:
   - `.object.yaml` → `request.object`
   - `.oldObject.yaml` → `request.oldObject`
   - Infers `name`, `namespace`, `kind`, `resource` from resource metadata
   - Infers `operation` from files present (unless specified in `.request.yaml`)
   - Adds `userInfo`, `namespaceObject`, `dryRun`, etc. from `.request.yaml`
4. Passes complete `AdmissionRequest` to the CEL evaluator (same library Kubernetes uses internally)
5. Evaluates policy CEL expressions against this context
6. If the policy references a `request.*` field that wasn't provided or inferred, the test fails with a clear error message

## Examples

### Example 1: MutatingAdmissionPolicy Test

**Input file:**
`policies/sidecar-injection/tests/sidecar-injection.adding-istio-sidecar.object.yaml`

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: app
    image: nginx
```

**Expected output:** `policies/sidecar-injection/tests/sidecar-injection.adding-istio-sidecar.gold.yaml`

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  initContainers:
  - name: istio-init
    image: istio/proxyv2:1.20.0
    # ... sidecar config
  containers:
  - name: app
    image: nginx
```

**Operation:** Inferred as `CREATE` (only `.object.yaml` present, no `.request.yaml`)

### Example 2: ValidatingAdmissionPolicy - Allow

**File:** `policies/require-owner-label/tests/require-owner-label.with-label.allow.object.yaml`

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
  labels:
    owner: team-platform
spec:
  containers:
  - name: app
    image: nginx
```

This test expects the policy to **allow** this Pod because it has the required `owner` label.

**Operation:** Inferred as `CREATE` (only `.object.yaml` present, no `.request.yaml`)

### Example 3: ValidatingAdmissionPolicy - Deny

**File:** `policies/require-owner-label/tests/require-owner-label.without-label.deny.object.yaml`

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
  # No owner label - should be denied
spec:
  containers:
  - name: app
    image: nginx
```

This test expects the policy to **deny** this Pod because it's missing the required `owner` label.

**Operation:** Inferred as `CREATE` (only `.object.yaml` present, no `.request.yaml`)

### Example 4: ValidatingAdmissionPolicy with Request Context

**Resource:** `policies/block-team-ci-service-accounts/tests/block-team-ci.blocked.deny.object.yaml`

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: app
    image: nginx
```

**Request context:** `policies/block-team-ci-service-accounts/tests/block-team-ci.blocked.deny.request.yaml`

```yaml
operation: CREATE
userInfo:
  username: "system:serviceaccount:team-ci-random:default"
  groups: ["system:serviceaccounts", "system:authenticated"]
```

This test validates that service accounts from non-core `team-ci-*` namespaces are blocked.

**Operation:** Explicitly set to `CREATE` in `.request.yaml`

### Example 5: ValidatingAdmissionPolicy with Namespace Context

**Resource:** `policies/require-prod-label/tests/require-prod-label.prod-namespace.allow.object.yaml`

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
  namespace: production
spec:
  containers:
  - name: app
    image: nginx
```

**Request context:** `policies/require-prod-label/tests/require-prod-label.prod-namespace.allow.request.yaml`

```yaml
namespace: production
namespaceObject:
  apiVersion: v1
  kind: Namespace
  metadata:
    name: production
    labels:
      environment: production
      team: platform
```

This test validates that the policy correctly evaluates namespace labels when making admission decisions.

**Operation:** Inferred as `CREATE` (only `.object.yaml` present)

### Example 6: ValidatingAdmissionPolicy for DELETE Operation

**Resource being deleted:** `policies/delete-protection/tests/delete-protection.protected-resource.deny.oldObject.yaml`

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: critical-configmap
  namespace: production
  labels:
    protected: "true"
data:
  config: important-value
```

**Request context:** `policies/delete-protection/tests/delete-protection.protected-resource.deny.request.yaml`

```yaml
operation: DELETE
userInfo:
  username: "user@example.com"
  groups: ["system:authenticated"]
```

This test validates that the policy blocks deletion of resources with the `protected: "true"` label.

**Operation:** Explicitly set to `DELETE` in `.request.yaml` (also inferred from only `.oldObject.yaml` being present)

**Note:** For DELETE operations:

- `.oldObject.yaml` contains the resource being deleted → `request.oldObject`
- `request.object` is null
- `name` and `namespace` are inferred from `.oldObject.yaml` metadata

### Example 7: ValidatingAdmissionPolicy for UPDATE Operation

**Old state:** `policies/prevent-owner-change/tests/prevent-owner-change.changing-owner.deny.oldObject.yaml`

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
  namespace: production
  labels:
    owner: team-platform
data:
  config: value
```

**New state:** `policies/prevent-owner-change/tests/prevent-owner-change.changing-owner.deny.object.yaml`

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
  namespace: production
  labels:
    owner: team-security  # Changed!
data:
  config: value
```

**Request context (optional):** `policies/prevent-owner-change/tests/prevent-owner-change.changing-owner.deny.request.yaml`

```yaml
operation: UPDATE
userInfo:
  username: "user@example.com"
  groups: ["system:authenticated"]
```

This test validates that the policy blocks changes to the `owner` label.

**Operation:** Inferred as `UPDATE` (both `.object.yaml` and `.oldObject.yaml` present)

**Note:** For UPDATE operations:

- `.oldObject.yaml` contains the previous state → `request.oldObject`
- `.object.yaml` contains the new state → `request.object`
- Both `name` and `namespace` must match between old and new objects
- `.request.yaml` is optional if no additional context is needed

### Example 8: ValidatingAdmissionPolicy for CONNECT Operation

This test set validates that `kubectl exec` into Pods in a production namespace is denied for non-admin users and allowed for admins.

**Policy:** `policies/block-pod-exec/policy.yaml`

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: block-pod-exec
spec:
  validations:
  - expression: |
      request.operation != 'CONNECT' ||
      request.subResource != 'exec' ||
      !has(namespaceObject.metadata.labels) ||
      namespaceObject.metadata.labels['environment'] != 'production' ||
      (has(request.userInfo.groups) && 'cluster-admins' in request.userInfo.groups)
    message: "kubectl exec to production pods requires cluster-admins"
```

**Binding:** `policies/block-pod-exec/binding.yaml`

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: block-pod-exec-binding
spec:
  policyName: block-pod-exec
  validationActions: [Deny]
```

**Deny request context:** `policies/block-pod-exec/tests/block-pod-exec.prod-non-admin.deny.request.yaml`

```yaml
operation: CONNECT
subResource: exec
name: app-pod
namespace: production
namespaceObject:
  apiVersion: v1
  kind: Namespace
  metadata:
    name: production
    labels:
      environment: production
userInfo:
  username: "user@example.com"
  groups: ["system:authenticated"]
options:
  container: app
  command: ["sh", "-c", "echo hello"]
  stdin: false
  stdout: true
  tty: false
```

**Allow request context:** `policies/block-pod-exec/tests/block-pod-exec.prod-admin.allow.request.yaml`

```yaml
operation: CONNECT
subResource: exec
name: app-pod
namespace: production
namespaceObject:
  apiVersion: v1
  kind: Namespace
  metadata:
    name: production
    labels:
      environment: production
userInfo:
  username: "admin@example.com"
  groups: ["system:authenticated", "cluster-admins"]
options:
  container: app
  command: ["sh", "-c", "echo hello"]
  stdin: false
  stdout: true
  tty: false
```

This pair validates both deny and allow paths for `CONNECT`.

**Operation:** Explicitly set to `CONNECT` in `.request.yaml` (cannot be inferred).

**Note:** For CONNECT operations:

- There is no `.object.yaml` or `.oldObject.yaml`; `request.object` is null.
- Provide `name`, `namespace`, `subResource` (e.g., `exec`), and `options` (e.g., `PodExecOptions`).

## Command-Line Interface

Following `go test` conventions for familiarity.

### Basic Usage

```bash
# Run all tests in current directory and subdirectories
kat ./...

# Run tests in specific directory
kat ./policies/sidecar-injection

# Run tests in current directory only
kat .

# Run specific test file
kat ./policies/sidecar-injection/tests/sidecar-injection.adding-istio-sidecar.object.yaml
```

### Flags

```bash
# Verbose output - show details for each test
kat -v ./...

# Run tests matching pattern
kat -run sidecar ./...
kat -run "sidecar.*istio" ./policies

# Show only failures (default shows summary)
kat -q ./...
```

### Test Discovery

The tool automatically discovers:

1. All `*.object.yaml` and `*.oldObject.yaml` files in specified directories
2. Matches pairs (mutating) and single-file cases (validating/delete):
  - `*.object.yaml` + `*.gold.yaml` for MutatingAdmissionPolicy
  - `*.allow.object.yaml` for ValidatingAdmissionPolicy (expects allow)
  - `*.deny.object.yaml` for ValidatingAdmissionPolicy (expects deny)
  - `*.allow.oldObject.yaml` for DELETE operations (expects allow)
  - `*.deny.oldObject.yaml` for DELETE operations (expects deny)
3. Finds corresponding `*.request.yaml` sidecars for additional context (including standalone requests for `operation: CONNECT`)
4. Locates `policy.yaml` and `binding.yaml` in parent directories
5. Infers operation from files present (CREATE/UPDATE/DELETE). `CONNECT` is not inferred and must be explicitly set in `.request.yaml`.

### Output Format

Following `go test` output style:

```text
=== RUN   sidecar-injection/adding-istio-sidecar
--- PASS: sidecar-injection/adding-istio-sidecar (0.12s)
=== RUN   sidecar-injection/skip-when-exists
--- PASS: sidecar-injection/skip-when-exists (0.08s)
=== RUN   require-owner-label/with-label
--- PASS: require-owner-label/with-label (0.05s)
=== RUN   require-owner-label/without-label
--- FAIL: require-owner-label/without-label (0.06s)
    Expected: denied
    Actual:   allowed
PASS
FAIL
ok      policies/sidecar-injection    0.20s
FAIL    policies/require-owner-label  0.11s
```

### Exit Codes

- `0` - All tests passed
- `1` - One or more tests failed
- `2` - Invalid arguments or configuration error

## Advanced Features

### Testing Validation Messages

For ValidatingAdmissionPolicy tests, you can assert on the expected error message:

**File:** `policies/require-owner-label/tests/require-owner-label.without-label.deny.object.yaml`

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: app
    image: nginx
```

**Expected message:** `policies/require-owner-label/tests/require-owner-label.without-label.deny.message.txt`

```text
Pod must have an 'owner' label
```

If the `.message.txt` file exists, the tool will verify that the policy's denial message matches the expected text.

### Testing with Parameters (ParamKind)

Some policies use external configuration via `paramKind`. Provide the parameter resource in a `.params.yaml` file:

**Policy with paramKind:** `policies/require-labels/policy.yaml`

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: require-labels
spec:
  paramKind:
    apiVersion: v1
    kind: ConfigMap
  validations:
  - expression: |
      params.data.requiredLabels.split(',').all(label,
        has(object.metadata.labels) && label in object.metadata.labels
      )
    message: "Missing required labels"
```

**Binding with paramRef:** `policies/require-labels/binding.yaml`

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: require-labels-binding
spec:
  policyName: require-labels
  validationActions: [Deny]
  paramRef:
    name: label-config
    namespace: default
    parameterNotFoundAction: Deny
```

**Test resource:** `policies/require-labels/tests/require-labels.missing-label.deny.object.yaml`

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
  labels:
    owner: team-platform
    # Missing 'team' and 'environment' labels
spec:
  containers:
  - name: app
    image: nginx
```

**Parameter resource:** `policies/require-labels/tests/require-labels.missing-label.deny.params.yaml`

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: label-config
  namespace: default
data:
  requiredLabels: "owner,team,environment"
```

**How it works:**

1. The tool loads the `.params.yaml` file
2. The parameter resource is made available as `params` in CEL expressions
3. The policy evaluates `params.data.requiredLabels.split(',')...`
4. The test expects the resource to be **denied** because it's missing required labels

**Testing different parameter values:**

You can test the same policy with different parameters by creating multiple test cases:

```text
policies/require-labels/tests/
├── require-labels.missing-label.deny.object.yaml
├── require-labels.missing-label.deny.params.yaml  # Requires owner,team,environment
├── require-labels.has-all-labels.allow.object.yaml
├── require-labels.has-all-labels.allow.params.yaml  # Requires owner,team,environment
├── require-labels.strict-mode.deny.object.yaml
└── require-labels.strict-mode.deny.params.yaml  # Requires owner,team,environment,cost-center
```

**Testing with CRD parameters:**

The same approach works with Custom Resource Definitions:

**Policy:** `policies/replica-limit/policy.yaml`

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: replica-limit
spec:
  paramKind:
    apiVersion: rules.example.com/v1
    kind: ReplicaLimit
  validations:
  - expression: "object.spec.replicas <= params.maxReplicas"
    message: "Too many replicas"
```

**Parameter CRD:** `policies/replica-limit/tests/replica-limit.too-many.deny.params.yaml`

```yaml
apiVersion: rules.example.com/v1
kind: ReplicaLimit
metadata:
  name: replica-limit-test
maxReplicas: 3
```

**Note:** The tool doesn't need the CRD definition installed - it just loads the parameter YAML and makes it available as `params`.

#### Complete Example: Testing Policy with Parameters

Here's a complete example showing the full directory structure:

```text
policies/replica-limit/
├── policy.yaml                    # The ValidatingAdmissionPolicy
├── binding.yaml                   # The ValidatingAdmissionPolicyBinding
└── tests/
    ├── replica-limit.within-limit.allow.object.yaml
    ├── replica-limit.within-limit.allow.params.yaml
    ├── replica-limit.exceeds-limit.deny.object.yaml
    ├── replica-limit.exceeds-limit.deny.params.yaml
    ├── replica-limit.no-params.deny.object.yaml
    └── replica-limit.no-params.deny.message.txt
```

**Test 1: Within limit (should allow)**

`replica-limit.within-limit.allow.object.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: small-deployment
spec:
  replicas: 2
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
```

`replica-limit.within-limit.allow.params.yaml`:

```yaml
apiVersion: rules.example.com/v1
kind: ReplicaLimit
metadata:
  name: replica-limit-test
maxReplicas: 5
```

**Test 2: Exceeds limit (should deny)**

`replica-limit.exceeds-limit.deny.object.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: large-deployment
spec:
  replicas: 10  # Exceeds limit of 5
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
```

`replica-limit.exceeds-limit.deny.params.yaml`:

```yaml
apiVersion: rules.example.com/v1
kind: ReplicaLimit
metadata:
  name: replica-limit-test
maxReplicas: 5
```

**Test 3: Missing params (should deny with specific message)**

`replica-limit.no-params.deny.object.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: any-deployment
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
```

`replica-limit.no-params.deny.message.txt`:

```txt
params missing but required to bind to this policy
```

**Note:** No `.params.yaml` file for this test - it validates the policy's behavior when params are missing.

**Running the tests:**

```bash
$ kat ./policies/replica-limit/tests/

=== RUN   replica-limit/within-limit
--- PASS: replica-limit/within-limit (0.05s)
=== RUN   replica-limit/exceeds-limit
--- PASS: replica-limit/exceeds-limit (0.06s)
=== RUN   replica-limit/no-params
--- PASS: replica-limit/no-params (0.04s)
PASS
ok      policies/replica-limit    0.15s
```

### Testing ValidationActions (Warn vs Deny)

ValidatingAdmissionPolicyBinding can specify different actions:

- `Deny` - Reject the request (default for `.deny.` tests)
- `Warn` - Allow but return warnings (use `.warn.` suffix)
- `Audit` - Allow and log (use `.audit.` suffix)

#### Testing Warn Action

For policies with `validationActions: [Warn]`, use the `.warn.` suffix:

**Resource:** `policies/deprecated-api/tests/deprecated-api.old-version.warn.object.yaml`

```yaml
apiVersion: apps/v1beta1  # Deprecated
kind: Deployment
metadata:
  name: old-deployment
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: nginx
        image: nginx
```

**Expected warning:** `policies/deprecated-api/tests/deprecated-api.old-version.warn.warnings.txt`

```text
Using deprecated API version apps/v1beta1
```

**How it works:**

1. The tool evaluates the policy with `validationActions: [Warn]`
2. If validation fails, the request is **allowed** but warnings are captured
3. If `.warnings.txt` exists, the tool verifies the warning message matches
4. The test **passes** if:
   - The resource is allowed (not denied)
   - The expected warning is present (if `.warnings.txt` provided)
5. The test **fails** if:
   - The resource is denied (should be allowed with warning)
   - The warning message doesn't match (if `.warnings.txt` provided)

#### Testing Audit Action

For policies with `validationActions: [Audit]`, use the `.audit.` suffix:

**Resource:** `policies/track-privileged/tests/track-privileged.privileged-pod.audit.object.yaml`

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: privileged-pod
spec:
  containers:
  - name: app
    image: nginx
    securityContext:
      privileged: true
```

**Expected audit annotation:** `policies/track-privileged/tests/track-privileged.privileged-pod.audit.annotations.yaml`

```yaml
high-privilege-pod: "Pod privileged-pod has privileged container: app"
```

**How it works:**

1. The tool evaluates the policy with `validationActions: [Audit]`
2. The request is **always allowed** (audit doesn't block)
3. If validation fails, audit annotations are captured
4. If `.annotations.yaml` exists, the tool verifies the annotations match
5. The test **passes** if:
   - The resource is allowed
   - The expected audit annotations are present (if `.annotations.yaml` provided)
6. The test **fails** if:
   - The audit annotations don't match (if `.annotations.yaml` provided)

**Note:** The audit annotation keys in the `.annotations.yaml` file should **not** include the policy name prefix - the tool will automatically add it when comparing.

### Testing MatchConditions

Policies can use `matchConditions` to conditionally apply. The tool evaluates these automatically based on the AdmissionRequest context.

**Example policy:**

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: prod-only-policy
spec:
  matchConditions:
  - name: is-production
    expression: |
      has(request.namespace) &&
      namespaceObject.metadata.labels['environment'] == 'production'
  validations:
  - expression: |
      object.metadata.labels['critical'] == 'true'
```

**Test:** Provide `namespaceObject` in `.request.yaml` to control whether matchConditions apply:

```yaml
namespace: production
namespaceObject:
  apiVersion: v1
  kind: Namespace
  metadata:
    name: production
    labels:
      environment: production  # This makes matchCondition pass
```

**Important:** If matchConditions don't match, the policy is skipped entirely:

- For `.allow.` tests: The test **passes** (resource is allowed because policy didn't apply)
- For `.deny.` tests: The test **fails** (policy was expected to deny but didn't evaluate)
- For `.warn.` tests: The test **fails** (policy was expected to warn but didn't evaluate)
- For `.audit.` tests: The test **fails** (policy was expected to audit but didn't evaluate)

This ensures your tests actually validate that the policy is being applied. If you want to test that a policy is correctly skipped due to matchConditions, use an `.allow.` test.

### Testing Multiple Validations

Policies can have multiple validation rules. The tool evaluates all of them:

```yaml
validations:
- expression: has(object.metadata.labels.owner)
  message: "Must have owner label"
- expression: has(object.metadata.labels.team)
  message: "Must have team label"
- expression: object.metadata.labels.environment in ['dev', 'staging', 'prod']
  message: "Environment must be dev, staging, or prod"
```

For `.deny.` tests, the tool expects **at least one** validation to fail. The `.message.txt` file can match any of the failure messages.

## Implementation Notes

### CEL Evaluation

The tool uses the same CEL libraries as Kubernetes:

- `k8s.io/apiserver/pkg/cel` - CEL environment setup
- `k8s.io/apiserver/pkg/admission/plugin/policy/validating` - Validation logic
- `k8s.io/apiserver/pkg/admission/plugin/policy/mutating` - Mutation logic

This ensures 100% compatibility with how policies behave in real clusters.

### Resource Inference

The tool infers several AdmissionRequest fields from the test resource:

- `name` - From `metadata.name`
- `namespace` - From `metadata.namespace`
- `kind` - From `kind`
- `apiVersion` - From `apiVersion`
- `resource` - Derived from `kind` (e.g., Pod → pods, Deployment → deployments)
- `uid` - Auto-generated UUID for the admission request

These can be overridden in `.request.yaml` if needed.

### Test Result Logic

The tool determines test pass/fail based on the test file suffix and policy evaluation result:

#### For `.allow.` tests (ValidatingAdmissionPolicy)

| Policy Result             | Test Result | Reason                                  |
|---------------------------|-------------|-----------------------------------------|
| Allowed                   | ✅ PASS      | Expected behavior                       |
| Denied                    | ❌ FAIL      | Policy denied when it should allow      |
| Skipped (matchConditions) | ✅ PASS      | Resource allowed (policy didn't apply)  |
| Error (CEL evaluation)    | ❌ FAIL      | Policy error (depends on failurePolicy) |

#### For `.deny.` tests (ValidatingAdmissionPolicy)

| Policy Result             | Test Result              | Reason                                   |
|---------------------------|--------------------------|------------------------------------------|
| Denied                    | ✅ PASS                   | Expected behavior                        |
| Allowed                   | ❌ FAIL                   | Policy allowed when it should deny       |
| Skipped (matchConditions) | ❌ FAIL                   | Policy didn't evaluate (test is invalid) |
| Error (CEL evaluation)    | Depends on failurePolicy | Fail→PASS, Ignore→FAIL                   |

#### For `.warn.` tests (ValidatingAdmissionPolicy)

| Policy Result             | Test Result | Reason                                   |
|---------------------------|-------------|------------------------------------------|
| Allowed + Warning         | ✅ PASS      | Expected behavior                        |
| Allowed (no warning)      | ❌ FAIL      | Validation passed (should have warned)   |
| Denied                    | ❌ FAIL      | Wrong validationAction (should be Warn)  |
| Skipped (matchConditions) | ❌ FAIL      | Policy didn't evaluate (test is invalid) |

#### For `.audit.` tests (ValidatingAdmissionPolicy)

| Policy Result             | Test Result | Reason                                   |
|---------------------------|-------------|------------------------------------------|
| Allowed + Audit           | ✅ PASS      | Expected behavior                        |
| Allowed (no audit)        | ❌ FAIL      | Validation passed (should have audited)  |
| Denied                    | ❌ FAIL      | Wrong validationAction (should be Audit) |
| Skipped (matchConditions) | ❌ FAIL      | Policy didn't evaluate (test is invalid) |

#### For MutatingAdmissionPolicy tests

| Policy Result                     | Test Result | Reason                                        |
|-----------------------------------|-------------|-----------------------------------------------|
| Mutated (matches .gold.yaml)      | ✅ PASS      | Expected behavior                             |
| Mutated (differs from .gold.yaml) | ❌ FAIL      | Unexpected mutation                           |
| Not mutated                       | ❌ FAIL      | Policy didn't mutate (check matchConstraints) |
| Skipped (matchConditions)         | ❌ FAIL      | Policy didn't evaluate (test is invalid)      |
| Error (CEL evaluation)            | ❌ FAIL      | Policy error                                  |

**Key principle:** If a test expects a policy to take action (deny, warn, audit, mutate), but the policy is skipped due to matchConditions or matchConstraints, the test **fails**. This ensures tests actually validate policy behavior.

### Error Handling

The tool provides clear error messages for common issues:

- **Missing policy file** - "No policy.yaml found in parent directories"
- **Missing required field** - "Policy references request.userInfo.username but no userInfo provided in .request.yaml"
- **Invalid YAML** - Shows parse error with line number
- **CEL evaluation error** - Shows the CEL expression and error message
- **Unexpected result** - Shows diff between expected and actual (for mutations) or expected vs actual decision (for validations)
- **Policy skipped** - "Policy was not evaluated due to matchConditions/matchConstraints not matching (test may be misconfigured)"

### Snapshot Testing

For complex mutations, snapshot testing could be useful:

```bash
# Update all .gold.yaml files with current output
kat -update ./policies/...
```

This would run all mutation tests and update the `.gold.yaml` files with the actual output, similar to Jest's snapshot testing.
