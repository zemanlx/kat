# kat test templates

Copy-ready file sets, one per archetype. **Rename** each file to
`<policy-name>.<test-name>.<expect>.<type>.yaml` before placing it in a `tests/`
directory (drop the `<policy-name>.` prefix if the directory has a single policy).
The `<expect>` token is a **validating** concept; for the **Mutation** archetype
omit it — mutating policies always allow, so name the case after what it mutates
(`<policy-name>.<test-name>.<type>.yaml`) and assert with `.gold.yaml`.

| Archetype | Files |
|---|---|
| Allow | `allow.object.yaml` |
| Deny | `deny.object.yaml` + `deny.message.txt` |
| Warn | `warn.object.yaml` + `warn.warnings.txt` |
| Audit | `audit.object.yaml` + `audit.annotations.yaml` |
| Mutation | `mutation.object.yaml` + `mutation.gold.yaml` |
| Update | `update.request.yaml` |
| Delete | `delete.oldObject.yaml` (+ optional `.message.txt`) |
| Params | `params.params.yaml` (+ your own `.object.yaml`) |
| UserInfo | `userinfo.request.yaml` |
| NamespaceObject | `namespaceobject.request.yaml` |
| Exec / CONNECT | `exec.request.yaml` |
| Authorizer | `authorizer.authorizer.yaml` (+ a `.request.yaml`/`.object.yaml`) |

Example rename for a policy named `require-owner-label`:

```
allow.object.yaml     ->  require-owner-label.with-label.allow.object.yaml
deny.object.yaml      ->  require-owner-label.without-label.deny.object.yaml
deny.message.txt      ->  require-owner-label.without-label.deny.message.txt
mutation.object.yaml  ->  add-default-labels.no-labels.object.yaml   # mutating: no expect token
mutation.gold.yaml    ->  add-default-labels.no-labels.gold.yaml
```

Then edit the object so it matches the policy's `matchConstraints` and exercises
the branch under test, and set the message/warnings/annotations/gold to the values
your policy actually produces. Finish with `kat -v <policy-dir>` and confirm exit 0.
