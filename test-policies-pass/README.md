# Test Policies for `kat` (Kubernetes Admission Tester)

This directory contains comprehensive test policies covering all major features of Kubernetes ValidatingAdmissionPolicy and MutatingAdmissionPolicy. These policies serve as:

1. **End-to-end test cases** for the `kat` tool implementation
2. **Reference examples** for policy authors
3. **Behavior lock** to ensure consistent policy evaluation

## Policy Categories

### Mutating Policies (`mutating/`)

#### `sidecar-injection/` (MutatingAdmissionPolicy)

**Purpose:** Injects Istio sidecar into pods with specific label.

**Features tested:**

- MutatingAdmissionPolicy
- JSONPatch mutations
- `matchConditions` for conditional mutation
- `.gold.yaml` for expected output

**Test cases:**

- 🔧 `adding-istio-sidecar` - Pod with sidecar.istio.io/inject: true (sidecar added)
- 🔧 `skip-without-label` - Pod without label (no mutation)

---

#### `add-default-labels/` (MutatingAdmissionPolicy)

**Purpose:** Adds default environment label if missing.

**Features tested:**

- Conditional mutations
- Label addition
- Preserving existing labels

**Test cases:**

- 🔧 `no-labels` - Deployment without labels (environment: dev added)
- 🔧 `has-environment` - Deployment with environment: prod (no change)

---

#### `mutating-with-binding/` (MutatingAdmissionPolicy with Binding)

**Purpose:** Adds labels from ConfigMap parameter using MutatingAdmissionPolicyBinding.

**Features tested:**

- MutatingAdmissionPolicyBinding with `paramRef`
- Conditional mutations with null-safe parameter checking
- Dynamic label addition from parameters
- JSONPatch mutations with parameter data

**Test cases:**

- 🔧 `add-label` - Deployment with params containing label (team: platform added)
- 🔧 `no-params` - Deployment without params (no mutation, safely handled)

---

### Validating Policies (`validating/`)

#### `require-owner-label/`

**Purpose:** Validates that all workloads have an 'owner' label.

**Features tested:**

- Basic CEL expression validation
- Label presence checking
- Static error messages
- Namespace selector in binding

**Test cases:**

- ✅ `with-label.allow` - Deployment with owner label (should pass)
- ❌ `without-label.deny` - Deployment without owner label (should fail with message)

---

#### `check-authorizer/` (Validating with Authorizer Check)

**Purpose:** Validates that the user has specific RBAC permissions (SubjectAccessReview).

**Features tested:**

- `authorizer` variable access in CEL
- `authorizer.check(...).allowed()`
- Mocking Authorizer responses with `.authorizer.yaml`

**Test cases:**

- ✅ `allowed` - User has mocked 'create pods' permission
- ❌ `denied` - User lacks mocked 'create pods' permission

---

#### `replica-limit/`

**Purpose:** Enforces a maximum replica count of 10.

**Features tested:**

- Numeric comparison in CEL
- `messageExpression` for dynamic error messages
- Cluster-wide binding

**Test cases:**

- ✅ `within-limit.allow` - 5 replicas (should pass)
- ❌ `exceeds-limit.deny` - 15 replicas (should fail with dynamic message)

---

#### `block-privileged-containers/`

**Purpose:** Prevents creation of privileged containers.

**Features tested:**

- Complex CEL expressions with nested fields
- Multiple validations in one policy
- Handling both Pods and workload resources (Deployments, StatefulSets, DaemonSets)

**Test cases:**

- ✅ `unprivileged-pod.allow` - Pod with privileged: false
- ❌ `privileged-pod.deny` - Pod with privileged: true
- ❌ `privileged-deployment.deny` - Deployment with privileged container

---

#### `block-team-ci-service-accounts/`

**Purpose:** Blocks team CI service accounts except core-infra-ci.

**Features tested:**

- `request.userInfo` access
- String manipulation (startsWith, endsWith)
- Service account filtering

**Test cases:**

- ✅ `allowed-core-infra.allow` - Request from system:serviceaccount:team-core-infra-ci:deployer
- ❌ `blocked-team-ci.deny` - Request from system:serviceaccount:team-platform-ci:deployer

---

#### `namespace-based-validation/`

**Purpose:** Requires production deployments to have at least 2 replicas.

**Features tested:**

- `namespaceObject` access
- Namespace label-based logic
- Conditional validation based on namespace

**Test cases:**

- ✅ `prod-ha.allow` - 3 replicas in prod namespace
- ❌ `prod-single-replica.deny` - 1 replica in prod namespace
- ✅ `dev-single-replica.allow` - 1 replica in dev namespace (policy doesn't apply)

---

#### `block-pod-exec/`

**Purpose:** Restricts `kubectl exec` access in production namespaces.

**Features tested:**

- `CONNECT` operation handling
- `subResource` checking ('exec')
- Complex boolean logic combining `userInfo`, `namespaceObject`, and request attributes

**Test cases:**

- ✅ `prod-admin.allow` - Admin connecting to prod pod
- ❌ `prod-non-admin.deny` - Non-admin connecting to prod pod

---

#### `delete-protection/`

**Purpose:** Prevents deletion of resources with 'protect: true' label.

**Features tested:**

- DELETE operation handling
- `oldObject` access
- Label-based protection

**Test cases:**

- ❌ `protected-namespace.deny` - DELETE namespace with protect: true
- ✅ `unprotected-namespace.allow` - DELETE namespace without protect label

---

#### `prevent-owner-change/`

**Purpose:** Prevents changing the owner label on UPDATE operations.

**Features tested:**

- UPDATE operation handling
- Both `object` and `oldObject` access
- Immutability enforcement
- Dynamic message with old and new values

**Test cases:**

- ✅ `same-owner.allow` - UPDATE with same owner label
- ❌ `changed-owner.deny` - UPDATE changing owner from platform-team to security-team

---

#### `replica-limit-with-params/`

**Purpose:** Enforces replica limit using ConfigMap parameter.

**Features tested:**

- `paramKind` with ConfigMap
- `params` variable access
- `paramRef` in binding
- `parameterNotFoundAction: Deny`
- Testing with and without parameters

**Test cases:**

- ✅ `within-limit.allow` - 3 replicas with maxReplicas: 5
- ❌ `exceeds-limit.deny` - 10 replicas with maxReplicas: 5
- ❌ `no-params.deny` - No params provided (should fail with specific message)

---

#### `require-labels-with-params/`

**Purpose:** Requires labels specified in ConfigMap parameter.

**Features tested:**

- String splitting and iteration in CEL
- Dynamic label requirements
- Complex parameter usage

**Test cases:**

- ✅ `has-all-labels.allow` - Has owner, team, environment labels
- ❌ `missing-label.deny` - Missing environment label

---

#### `deprecated-api-warn/` (Warn Action)

**Purpose:** Warns about deprecated API versions.

**Features tested:**

- `validationActions: [Warn]`
- Warning message generation
- `.warnings.txt` assertion

**Test cases:**

- ⚠️ `old-version.warn` - apps/v1beta1 Deployment (allowed with warning)

---

#### `track-privileged-audit/` (Audit Action)

**Purpose:** Audits privileged container usage.

**Features tested:**

- `validationActions: [Audit]`
- `auditAnnotations` with CEL expressions
- `.annotations.yaml` assertion
- Complex conditional audit annotation

**Test cases:**

- 📝 `privileged-pod.audit` - Privileged pod (allowed with audit annotation)
- 📝 `unprivileged-pod.audit` - Unprivileged pod (allowed without audit annotation)

---

#### `conditional-policy/` (matchConditions)

**Purpose:** Applies replica requirement only to production namespaces.

**Features tested:**

- `matchConditions` with namespace labels
- Policy skipping when conditions don't match
- Test failure when policy is expected to apply but is skipped

**Test cases:**

- ❌ `prod-ha.deny` - 1 replica in prod namespace (policy applies, fails)
- ✅ `dev-single-replica.allow` - 1 replica in dev namespace (policy skipped, allowed)

---

## Test File Naming Convention

All test files follow the naming pattern defined in the input format specification:

```text
<policy-name>.<test-name>.<suffix>.<type>.yaml
```

### Suffixes

- `.allow` - Resource should be allowed
- `.deny` - Resource should be denied
- `.warn` - Resource should be allowed with warning
- `.audit` - Resource should be allowed with audit annotation

### Types

- `.object.yaml` - The resource to test (request.object)
- `.oldObject.yaml` - Previous state for UPDATE/DELETE (request.oldObject)
- `.request.yaml` - Additional request context (userInfo, namespace, etc.)
- `.params.yaml` - Parameter resource for parameterized policies
- `.gold.yaml` - Expected output for mutations
- `.message.txt` - Expected error message
- `.warnings.txt` - Expected warning message
- `.annotations.yaml` - Expected audit annotations

## Running Tests

```bash
# Run all tests
kat ./test-policies-pass/

# Run specific policy tests
kat ./test-policies-pass/require-owner-label/tests/

# Run single test
kat ./test-policies-pass/require-owner-label/tests/require-owner-label.without-label.deny.object.yaml
```

## Coverage Matrix

| Feature                           | Policy Example                                                     |
|-----------------------------------|--------------------------------------------------------------------|
| Basic validation                  | `require-owner-label`, `replica-limit`                             |
| Complex CEL                       | `block-privileged-containers`                                      |
| Request context (userInfo)        | `block-team-ci-service-accounts`                                   |
| Request context (namespaceObject) | `namespace-based-validation`                                       |
| Authorizer Check                  | `check-authorizer`                                                 |
| CONNECT operation                 | `block-pod-exec`                                                   |
| DELETE operation                  | `delete-protection`                                                |
| UPDATE operation                  | `prevent-owner-change`                                             |
| Parameters (ConfigMap)            | `replica-limit-with-params`, `require-labels-with-params`          |
| Warn action                       | `deprecated-api-warn`                                              |
| Audit action                      | `track-privileged-audit`                                           |
| Mutations                         | `sidecar-injection`, `add-default-labels`, `mutating-with-binding` |
| Mutations with binding + params   | `mutating-with-binding`                                            |
| matchConditions                   | `conditional-policy`, `sidecar-injection`                          |
| messageExpression                 | `replica-limit`, `prevent-owner-change`                            |
| auditAnnotations                  | `track-privileged-audit`                                           |

## Expected Test Results

When all tests pass, you should see:

```text
=== RUN   require-owner-label/with-label
--- PASS: require-owner-label/with-label (0.05s)
=== RUN   require-owner-label/without-label
--- PASS: require-owner-label/without-label (0.05s)
...
PASS
ok      test-policies-pass    2.45s
```

All 30+ test cases should pass, validating the complete behavior of the `kat` tool.
