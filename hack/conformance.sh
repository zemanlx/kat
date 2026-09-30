#!/usr/bin/env bash
# Run the conformance tests in conformance/, which compare kat with a real
# kube-apiserver, against one or more Kubernetes versions.
#
# Usage:
#   ./hack/conformance.sh                 # every version in conformance/k8s-versions.txt
#   ./hack/conformance.sh 1.37.x          # one version, as a setup-envtest selector
#   ./hack/conformance.sh 1.36.x 1.37.x   # several versions, in parallel
#
# A pinned setup-envtest downloads kube-apiserver and etcd into bin/envtest
# (override with ENVTEST_DIR). Extra `go test` flags go in GOTESTFLAGS, for
# example GOTESTFLAGS="-v -run TestConformance/test-policies-pass/mutating".
# CONFORMANCE_PARALLEL caps how many apiservers run at once per version
# (default 4). The kat suites generated from the server's results are kept in
# conformance/.artifacts/<server version>; run `kat <dir>` on one to reproduce
# a binary-layer failure.
set -euo pipefail

setup_envtest_version="v0.25.1"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin_dir="${repo_root}/bin"
envtest_dir="${ENVTEST_DIR:-${bin_dir}/envtest}"
setup_envtest="${bin_dir}/setup-envtest-${setup_envtest_version}"

if [[ ! -x "${setup_envtest}" ]]; then
	echo "installing setup-envtest ${setup_envtest_version}"
	GOBIN="${bin_dir}" go install "sigs.k8s.io/controller-runtime/tools/setup-envtest@${setup_envtest_version}"
	mv "${bin_dir}/setup-envtest" "${setup_envtest}"
fi

if [[ $# -gt 0 ]]; then
	versions=("$@")
else
	mapfile -t versions < <(grep -Ev '^[[:space:]]*(#|$)' "${repo_root}/conformance/k8s-versions.txt")
fi

# Download one version at a time; setup-envtest's store is not safe for
# concurrent writers.
declare -A assets
for version in "${versions[@]}"; do
	assets["${version}"]="$("${setup_envtest}" use "${version}" --bin-dir "${envtest_dir}" -p path)"
	echo "kubernetes ${version}: ${assets[${version}]}"
done

run() {
	local version="$1"
	cd "${repo_root}/conformance"
	# shellcheck disable=SC2086 # GOTESTFLAGS is a list of flags.
	KUBEBUILDER_ASSETS="${assets[${version}]}" \
		go test -count=1 -timeout 30m -parallel "${CONFORMANCE_PARALLEL:-4}" ${GOTESTFLAGS:-} ./...
}

if [[ ${#versions[@]} -eq 1 ]]; then
	run "${versions[0]}"
	exit
fi

declare -A pids
for version in "${versions[@]}"; do
	run "${version}" 2>&1 | sed -u "s/^/[${version}] /" &
	pids["${version}"]=$!
done

failed=()
for version in "${versions[@]}"; do
	if ! wait "${pids[${version}]}"; then
		failed+=("${version}")
	fi
done

if [[ ${#failed[@]} -gt 0 ]]; then
	echo "conformance tests failed for: ${failed[*]}" >&2
	exit 1
fi

echo "conformance tests passed for: ${versions[*]}"
