#!/usr/bin/env bash
#
# Verify that every place the released version appears agrees with the chart's
# appVersion, which is the single source of truth. The release pipeline stamps
# the real version from the git tag at publish time, but the checked-in tree
# must still be internally consistent so a version bump that misses a file fails
# review instead of shipping a manifest that points at an image tag that was
# never pushed.
#
# Checks:
#   1. The chart's image tags (values.yaml) are empty (they inherit appVersion)
#      or equal appVersion. A non-empty tag that disagrees is drift.
#   2. The raw deployment manifests pin the ghcr image at exactly appVersion.
#   3. No manifest still references the retired internal ACR registry.
#
# Run on every CI build and locally with: bash scripts/check-versions.sh
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

chart="charts/safeguard-csi-provider/Chart.yaml"
values="charts/safeguard-csi-provider/values.yaml"
image_repo="ghcr.io/oneidentity/safeguard-csi-provider"
retired_registry="starlingdev.azurecr.io"

app_version="$(awk -F': *' '/^appVersion:/ { gsub(/[\r"'\'' ]/, "", $2); print $2; exit }' "$chart")"
if [ -z "${app_version}" ]; then
  echo "ERROR: could not read appVersion from ${chart}" >&2
  exit 1
fi
echo "Chart appVersion (source of truth): ${app_version}"

fail=0
drift() { echo "  DRIFT: $*" >&2; fail=1; }

# 1. Chart image tags must be empty (inherit appVersion) or match it exactly.
while IFS= read -r tag; do
  tag="$(printf '%s' "${tag}" | tr -d '\r' | tr -d '"'\'' ')"
  if [ -n "${tag}" ] && [ "${tag}" != "${app_version}" ]; then
    drift "${values} pins image tag '${tag}' but appVersion is '${app_version}' (leave tag empty to inherit appVersion)"
  fi
done < <(awk '/^[[:space:]]+tag:/ { print $2 }' "${values}")

# 2. Raw deployment manifests must reference the ghcr image at exactly appVersion.
found_refs=0
while IFS= read -r ref; do
  found_refs=1
  location="${ref%%:${image_repo}:*}"
  tag="$(printf '%s' "${ref##*:}" | tr -d '\r')"
  if [ "${tag}" != "${app_version}" ]; then
    drift "${location} pins image tag '${tag}' but appVersion is '${app_version}'"
  fi
done < <(grep -rnoE "${image_repo}:[A-Za-z0-9._-]+" deployment/ 2>/dev/null || true)

if [ "${found_refs}" -eq 0 ]; then
  drift "no ${image_repo} image references found under deployment/ (expected pinned installers)"
fi

# 3. The retired internal registry must not reappear in shipped manifests.
if grep -rn "${retired_registry}" charts/ deployment/ >/dev/null 2>&1; then
  drift "found retired registry '${retired_registry}'; public images must use '${image_repo}'"
fi

if [ "${fail}" -ne 0 ]; then
  echo "Version consistency check FAILED." >&2
  exit 1
fi
echo "Version consistency check passed; chart and manifests all pin ${app_version}."
