# Failing Test Policies for `kat` (Kubernetes Admission Tester)

**⚠️ IMPORTANT: ALL TESTS IN THIS DIRECTORY ARE EXPECTED TO FAIL ⚠️**

This directory contains test policies that are designed to produce failures during evaluation. These serve as **integration tests** for the `kat` tool itself, verifying that it correctly detects and reports various types of policy violations and test mismatches.

**DO NOT USE THESE AS EXAMPLES OF WORKING POLICIES.**

## Purpose

These tests verify that `kat` correctly provides:
1. **Accurate Error Reporting:** Clear messages when policies fail.
2. **Diff Visualization:** Readable diffs for mismatches (YAML objects, text files, maps).
3. **Exit Codes:** Non-zero exit codes on failure.

## Included Policy Tests

The following directories check different failure modes:

| Directory                         | Failure Mode Tested                                                                             |
|-----------------------------------|-------------------------------------------------------------------------------------------------|
| `add-default-labels/`             | **Mutation Mismatch**: The mutated object does not match the expected `.gold.yaml` file.        |
| `block-pod-exec/`                 | **Validation Logic**: Policy denies a request that was expected to be allowed.                  |
| `block-team-ci-service-accounts/` | **Request Context**: `userInfo` checks fail (service account handling).                         |
| `conditional-policy/`             | **Match Conditions**: Policy is enforced/skipped unexpectedly due to `matchConditions`.         |
| `deprecated-api-warn/`            | **Warning Mismatch**: The generated warning message differs from `.warnings.txt`.               |
| `mutating-with-binding/`          | **Binding Parameters**: Mutation logic using parameters produces incorrect output.              |
| `prevent-owner-change/`           | **OldObject Access**: Logic verifying `oldObject` vs `object` fails on update.                  |
| `track-privileged-audit/`         | **Audit Annotation Mismatch**: The generated audit annotations differ from `.annotations.yaml`. |

## Running These Tests

When running these tests, `kat` should output failures.

```bash
# Run all failing tests
kat ./test-policies-fail/
```

### Expected Output Format

You should see output indicating failures, typically with diffs showing the mismatch:

```text
=== RUN   track-privileged-audit/privileged-pod
    evaluator.go:123: audit annotations mismatch (-expected +actual):
        -fail: "true"
        -high-privilege-pod: 'Pod privileged-pod has privileged container: api'
        +high-privilege-pod: 'Pod privileged-pod has privileged container: app'
--- FAIL: track-privileged-audit/privileged-pod (0.05s)
```
