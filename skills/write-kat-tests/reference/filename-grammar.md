# kat filename grammar — authoritative reference

This is the precise specification of how `kat` interprets test file names and
directory layout, derived from the loader/evaluator source. Read this when
`SKILL.md` is not enough.

## Contents

- [Directory layout & discovery](#directory-layout--discovery)
- [Policy and binding filenames](#policy-and-binding-filenames)
- [Test filename grammar](#test-filename-grammar)
- [Input type suffixes](#input-type-suffixes)
- [Companion assertion files](#companion-assertion-files)
- [Policy-name matching](#policy-name-matching)
- [Expect token parsing](#expect-token-parsing)
- [Operation inference](#operation-inference)
- [Merging & conflicts](#merging--conflicts)
- [Assertion semantics](#assertion-semantics)
- [Run order & reporting](#run-order--reporting)
- [Constraints](#constraints)

## Directory layout & discovery

- `kat <path>` requires a **directory**; single files error (`os.ReadDir`).
- Discovery recurses to find directories that contain policy files. For each,
  policies/bindings are loaded from that directory and test cases from an adjacent
  `tests/` subdirectory.
- Directories named `tests`, `testdata`, or starting with `.` are skipped as
  suite roots.

## Policy and binding filenames

Policy files: `policy.yaml`, `policy.yml`, `policies.yaml`, `policies.yml`,
`*.policy.yaml`, `*.policy.yml`, `*.policies.yaml`, `*.policies.yml`.

Binding files: `binding.yaml`, `binding.yml`, `bindings.yaml`, `bindings.yml`,
`*.binding.yaml`, `*.binding.yml`, `*.bindings.yaml`, `*.bindings.yml`.

Multiple resources may live in one file separated by `---`.

## Test filename grammar

```
<policy-name>.<test-name>.<expect>.<type>.yaml
                                    ^^^^^^ (input suffix; see below)
base name = <policy-name>.<test-name>.<expect>
```

- The base name is everything with the recognized type suffix stripped.
- The test's reported name is `<base-name>.yaml`.
- All files sharing a base name are merged into one test case.

## Input type suffixes

Recognized as test **inputs** (8):

```
.request.yaml
.object.yaml
.oldObject.yaml
.namespaceObject.yaml
.params.yaml
.annotations.yaml
.warnings.txt
.authorizer.yaml
```

`.request.yaml` fields: `operation`, `object`, `oldObject`, `params`,
`namespaceObject`, `userInfo`, `namespace`, `name`, `subResource`, `options`.

## Companion assertion files

Found by string-substituting the input filename's `.object.yaml`/`.request.yaml`
suffix — they are **not** in the input list and are optional:

- `<base>.gold.yaml` — expected object after mutation. Its presence sets
  "expect mutated = true".
- `<base>.message.txt` — expected deny message (whitespace trimmed).

## Policy-name matching

For base name `B` and the directory's policy names:

1. If `B` starts with `<policyName>.`, that policy is selected.
2. Else, if the directory has exactly one policy, it is used regardless of prefix.
3. Else (multiple policies, no prefix match), no policy is associated.

So with a single-policy directory the `<policy-name>` prefix is effectively
optional; with multiple policies it is required and must match a policy's
`metadata.name` exactly.

## Expect token parsing

- Base name contains `.deny.` or ends with `.deny` → **expect denied**.
- Base name contains `.audit.` or `.warn.` → expect **allowed** (with side effects).
- Otherwise → expect **allowed** (default).

Only the `deny` token flips the allow/deny expectation. `warn`/`audit` are
allow-with-assertions and rely on their companion files for the real check.

## Operation inference

| object | oldObject | explicit `operation:` | result |
|---|---|---|---|
| ✓ | ✗ | — | CREATE |
| ✗ | ✓ | — | DELETE |
| ✓ | ✓ | — | UPDATE |
| ✗ | ✗ | — | error (cannot infer) |
| any | any | set | uses explicit value |

If an explicit `operation:` disagrees with the inferred value, `kat` errors.
CONNECT must be set explicitly (usually with an `object` present).

## Merging & conflicts

Files sharing a base name merge. Defining the same structural field
(`object`, `oldObject`, `namespaceObject`, `params`) in more than one file is an
error: `conflict: <field> defined in multiple files`.

## Assertion semantics

- **allow**: `Allowed == true`.
- **deny**: `Allowed == false`; if `.message.txt` present, message must equal it
  exactly (diff shown on mismatch).
- **warn**: `Allowed == true`; warnings compared by index against `.warnings.txt`
  (same count, each line exact).
- **audit**: `Allowed == true`; actual annotations filtered to the keys in
  `.annotations.yaml`, then compared for exact equality (extra keys ignored).
- **mutation**: if the policy mutates but no `.gold.yaml` exists, the case fails
  with "policy mutated the object but no .gold.yaml file was provided"; if
  `.gold.yaml` exists, actual vs expected objects are diffed as YAML.

## Run order & reporting

- Test cases run in alphabetical order of base name (`sort.Strings`).
- In `-json` output the `"package"` field is the **suite directory basename**
  (`filepath.Base(dir)`), and `"test"` is `<base-name>.yaml`.

## Constraints

- Only `admissionregistration.k8s.io/v1` policies/bindings are supported;
  `v1beta1` documents are a hard error.
- `kat` exits `0` when all tests pass, non-zero otherwise.
