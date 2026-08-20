---
name: write-kat-tests
description: >-
  Authoritative guide for writing test cases for kat, the local Kubernetes
  Admission Policy tester (ValidatingAdmissionPolicy and MutatingAdmissionPolicy).
  Use this whenever you create, edit, or debug kat tests, add test coverage for a
  Kubernetes admission policy, or work with files named *.object.yaml,
  *.request.yaml, *.gold.yaml, *.message.txt, *.warnings.txt, or *.annotations.yaml
  under a tests/ directory. Encodes the exact filename grammar, operation inference,
  and expectation semantics so tests pass on the first run.
---

# Writing kat tests

`kat` tests a Kubernetes admission policy by feeding it an admission request and
asserting the outcome. **The filename is the API**: the test's name, which policy
it targets, the expected outcome, and what each file contains are all encoded in
the filename. Get the name right and the test almost writes itself.

## Procedure: write → run → validate

Always follow this loop. Do not consider a test done until `kat` passes it.

1. **Locate the policy.** Find the directory containing `policy.yaml` (or
   `*.policy.yaml`). Read its `metadata.name`, `spec.matchConstraints` (which
   resources/operations it applies to), and its `validations`/`mutations`.
2. **Create `tests/` next to the policy** if it does not exist.
3. **Pick the archetype** (allow / deny / warn / audit / mutation) and copy the
   matching template from `reference/templates/`.
4. **Name the file** using the grammar (see below and `reference/filename-grammar.md`).
5. **Fill in a realistic object** that matches the policy's `matchConstraints`
   (right apiGroup/version/resource) and exercises the branch you are testing.
6. **Add the companion assertion file** if needed (`.message.txt` for deny,
   `.warnings.txt` for warn, `.annotations.yaml` for audit, `.gold.yaml` for
   mutation).
7. **Run and validate:**
   ```bash
   go build -o /tmp/kat . && /tmp/kat -v <policy-dir> ; echo "exit=$?"
   ```
   Fix until exit code is `0`. For a single case use `-run "<test-name>"`.

## Filename grammar (quick reference)

```
<policy-name>.<test-name>.<expect>.<type>.yaml
```

- **`<policy-name>`** — must prefix the policy's `metadata.name`. If the directory
  has exactly one policy, the prefix is optional (auto-associated). With multiple
  policies it is required.
- **`<test-name>`** — a descriptive slug (may contain dots).
- **`<expect>`** — `allow` | `deny` | `warn` | `audit`. Only `.deny.`/`.deny`
  means "expect denied"; everything else expects the request allowed.
- **`<type>`** — the input file kind (see table). Files that share the base name
  `<policy-name>.<test-name>.<expect>` are merged into one test case.

| type suffix | contents |
|---|---|
| `.object.yaml` | the object being admitted (most common) |
| `.request.yaml` | full AdmissionRequest (object + operation + userInfo + namespace + params …) |
| `.oldObject.yaml` | previous object (UPDATE/DELETE) |
| `.namespaceObject.yaml` | Namespace object for context |
| `.params.yaml` | param resource (`paramKind`/`paramRef`) |
| `.authorizer.yaml` | mocked SubjectAccessReview responses |
| `.warnings.txt` | expected warnings (assertion) |
| `.annotations.yaml` | expected audit annotations (assertion) |

Companion assertion files matched by base name (NOT input types):
`.gold.yaml` (expected mutated object) and `.message.txt` (expected deny message).

Full spec, edge cases, and citations: **`reference/filename-grammar.md`**.

## Choosing operation

Operation is inferred from which objects are present — do not set it unless needed:

- `object` only → **CREATE**
- `oldObject` only → **DELETE**
- both `object` and `oldObject` → **UPDATE**
- **CONNECT** → set `operation: CONNECT` in a `.request.yaml`

Setting `operation:` to something that conflicts with the inferred value is an error.

## Assertion semantics (match exactly)

- **allow** — request must be admitted.
- **deny** — request must be rejected. Optional `.message.txt` must equal the
  policy message (whitespace trimmed). Always add it to catch "denied for the
  wrong reason".
- **warn** — admitted with warnings; `.warnings.txt` matches warnings by index,
  one per line, exact text.
- **audit** — admitted; `.annotations.yaml` (key→value map) matches the listed
  keys exactly (extra actual annotations are ignored).
- **mutation** — a mutating policy that changes the object **requires** a
  `.gold.yaml` with the full expected object, or the case fails.

## Templates

Copy-ready file sets live in `reference/templates/`:

- `allow.object.yaml`
- `deny.object.yaml` + `deny.message.txt`
- `warn.object.yaml` + `warn.warnings.txt`
- `audit.object.yaml` + `audit.annotations.yaml`
- `mutation.object.yaml` + `mutation.gold.yaml`
- `update.request.yaml` (UPDATE / oldObject example)
- `delete.oldObject.yaml` (DELETE / oldObject-only example)

Advanced inputs (combine with an object, in the same base name):

- `params.params.yaml` — param resource for `paramKind`/`paramRef` policies
- `userinfo.request.yaml` — sets `request.userInfo` (username/groups)
- `namespaceobject.request.yaml` — sets `namespaceObject` (namespace labels/metadata)
- `exec.request.yaml` — CONNECT subresource (e.g. `kubectl exec`) with `subResource`/`options`
- `authorizer.authorizer.yaml` — mocks Authorizer (SubjectAccessReview) checks

Rename each to `<policy-name>.<test-name>.<expect>.<type>.yaml` before use.

## Common mistakes

- Pointing `kat` at a file — it takes **directories only**. Use `-run` for one case.
- Object doesn't match `matchConstraints`, so the policy never fires and a "deny"
  test wrongly passes as allow. Verify apiGroup/version/resource/operation.
- Wrong `<expect>` token (only `deny` flips the expectation).
- Mutating policy without a `.gold.yaml`.
- Same field defined in both `.request.yaml` and a split file → conflict error.
- Using `v1beta1` policies — only `admissionregistration.k8s.io/v1` is supported.
