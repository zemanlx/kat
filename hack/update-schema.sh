#!/usr/bin/env bash
# Regenerate the embedded Kubernetes OpenAPI schema used by internal/ssa.
#
# The schema drives server-side-apply merge behaviour, so it must match the
# k8s.io/* module versions in go.mod. This script derives the target Kubernetes
# release from k8s.io/apimachinery, downloads its OpenAPI v2 spec, strips it to
# the definitions block, and records the version so the drift guard in
# ssa_test.go can verify the two stay in sync. The schema is written as plain
# (pretty-printed) JSON so it stays reviewable in diffs.
#
# Run it after bumping the k8s.io/* dependencies:
#   ./hack/update-schema.sh
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ssa_dir="${repo_root}/internal/ssa"

# Module version, e.g. v0.37.0. Kubernetes tags the matching release as v1.37.0.
mod_version="$(cd "${repo_root}" && go list -m -f '{{.Version}}' k8s.io/apimachinery)"
release_tag="v1.${mod_version#v0.}"

url="https://raw.githubusercontent.com/kubernetes/kubernetes/${release_tag}/api/openapi-spec/swagger.json"
echo "k8s.io/apimachinery ${mod_version} -> kubernetes ${release_tag}"
echo "downloading ${url}"

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

curl -fsSL "${url}" -o "${tmp}/swagger.full.json"

# Keep only the definitions block (the type converter ignores paths, which make
# up the bulk of the upstream document) and pretty-print with sorted keys so the
# embedded schema stays reviewable and produces stable diffs.
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); json.dump({"swagger":d["swagger"],"info":d["info"],"definitions":d["definitions"]}, open(sys.argv[2],"w"), indent=2, sort_keys=True)' \
	"${tmp}/swagger.full.json" "${ssa_dir}/swagger.json"

printf '%s\n' "${mod_version}" > "${ssa_dir}/schema.version"

echo "wrote ${ssa_dir}/swagger.json ($(wc -c < "${ssa_dir}/swagger.json") bytes)"
echo "wrote ${ssa_dir}/schema.version (${mod_version})"
echo "done; run 'go test ./internal/ssa/...' to verify"
