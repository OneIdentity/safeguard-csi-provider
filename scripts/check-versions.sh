#!/usr/bin/env bash
#
# The chart's appVersion is the single source of truth for the released version.
# The release pipeline stamps the real version from the git tag at publish time,
# so NOTHING in the tree has to be edited to cut a release -- a `vX.Y.Z` tag
# stamps the image and the release artifacts by itself. The checked-in values are
# only a development baseline; this script keeps that baseline internally
# consistent so a bump that misses a file fails review instead of shipping a
# manifest that points at an image tag that was never pushed.
#
# Usage:
#   scripts/check-versions.sh                 # verify only (CI + local); read-only
#   scripts/check-versions.sh --fix           # rewrite chart version + deployment
#                                             # manifests to match appVersion
#   scripts/check-versions.sh --set-version X.Y.Z
#                                             # set appVersion (and chart version)
#                                             # to X.Y.Z, then align everything
#
# So bumping the baseline to the next release is a single command, e.g.:
#   scripts/check-versions.sh --set-version 0.4.0
#
# What is verified / aligned:
#   1. The chart `version` equals `appVersion` (the pipeline packages with both
#      set to the tag, so the tree keeps them equal too).
#   2. The chart image tags (values.yaml) are empty (they inherit appVersion) or
#      equal appVersion. A non-empty tag that disagrees is drift. (Report only --
#      never auto-emptied, so an intentional pin is never silently discarded.)
#   3. The raw deployment manifests pin the ghcr image at exactly appVersion.
#   4. No manifest still references the retired internal ACR registry.
#   5. On a release tag build (a v* tag ref in RELEASE_REF or BUILD_SOURCEBRANCH),
#      the tag version equals appVersion, so a tag can't publish while the tree
#      still names an older version. Branch, PR, and local runs skip this check.
set -euo pipefail

mode="check"
new_version=""
while [ $# -gt 0 ]; do
  case "$1" in
    --fix) mode="fix" ;;
    --set-version)
      mode="set"
      shift || true
      new_version="${1:-}"
      if [ -z "${new_version}" ]; then
        echo "ERROR: --set-version requires a version argument (e.g. 0.4.0)" >&2
        exit 2
      fi
      ;;
    -h|--help)
      sed -n '2,29p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument '$1' (see --help)" >&2
      exit 2
      ;;
  esac
  shift || true
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

chart="charts/safeguard-csi-provider/Chart.yaml"
values="charts/safeguard-csi-provider/values.yaml"
deploy_dir="deployment"
image_repo="ghcr.io/oneidentity/safeguard-csi-provider"
retired_registry="starlingdev.azurecr.io"

read_field() { # file, field  -> value with CR/quotes/space stripped
  awk -F': *' -v f="$2" '$0 ~ "^" f ":" { gsub(/[\r"'\'' ]/, "", $2); print $2; exit }' "$1"
}

write_file() { # stdin -> file (portable in-place)
  local dest="$1" tmp
  tmp="$(mktemp)"
  cat > "$tmp"
  mv "$tmp" "$dest"
}

set_chart_version() { # version
  sed -e "s/^version:.*/version: $1/" -e "s/^appVersion:.*/appVersion: $1/" "$chart" | write_file "$chart"
}

align_manifest_tags() { # version
  local f
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    sed -E "s#(${image_repo}):[A-Za-z0-9._-]+#\1:$1#g" "$f" | write_file "$f"
    echo "  aligned ${f} -> ${image_repo}:$1"
  done < <(grep -rlE "${image_repo}:[A-Za-z0-9._-]+" "$deploy_dir" 2>/dev/null || true)
}

if [ "$mode" = "set" ]; then
  if ! printf '%s' "${new_version}" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.]+)?$'; then
    echo "ERROR: '${new_version}' does not look like a version (expected e.g. 0.4.0)" >&2
    exit 2
  fi
  echo "Setting chart version + appVersion to ${new_version}"
  set_chart_version "${new_version}"
fi

app_version="$(read_field "$chart" appVersion)"
if [ -z "${app_version}" ]; then
  echo "ERROR: could not read appVersion from ${chart}" >&2
  exit 1
fi
chart_version="$(read_field "$chart" version)"
echo "Chart appVersion (source of truth): ${app_version}"

if [ "$mode" = "fix" ] || [ "$mode" = "set" ]; then
  if [ "${chart_version}" != "${app_version}" ]; then
    set_chart_version "${app_version}"
    chart_version="${app_version}"
    echo "  aligned ${chart} version -> ${app_version}"
  fi
  align_manifest_tags "${app_version}"
fi

fail=0
drift() { echo "  DRIFT: $*" >&2; fail=1; }

# 1. Chart version must equal appVersion.
if [ "${chart_version}" != "${app_version}" ]; then
  drift "${chart} version '${chart_version}' != appVersion '${app_version}' (run --fix)"
fi

# 2. Chart image tags must be empty (inherit appVersion) or match it exactly.
while IFS= read -r tag; do
  tag="$(printf '%s' "${tag}" | tr -d '\r' | tr -d '"'\'' ')"
  if [ -n "${tag}" ] && [ "${tag}" != "${app_version}" ]; then
    drift "${values} pins image tag '${tag}' but appVersion is '${app_version}' (leave tag empty to inherit appVersion)"
  fi
done < <(awk '/^[[:space:]]+tag:/ { print $2 }' "${values}")

# 3. Raw deployment manifests must reference the ghcr image at exactly appVersion.
found_refs=0
while IFS= read -r ref; do
  found_refs=1
  location="${ref%%:${image_repo}:*}"
  tag="$(printf '%s' "${ref##*:}" | tr -d '\r')"
  if [ "${tag}" != "${app_version}" ]; then
    drift "${location} pins image tag '${tag}' but appVersion is '${app_version}' (run --fix)"
  fi
done < <(grep -rnoE "${image_repo}:[A-Za-z0-9._-]+" "$deploy_dir" 2>/dev/null || true)

if [ "${found_refs}" -eq 0 ]; then
  drift "no ${image_repo} image references found under ${deploy_dir}/ (expected pinned installers)"
fi

# 4. The retired internal registry must not reappear in shipped manifests.
if grep -rn "${retired_registry}" charts/ "$deploy_dir"/ >/dev/null 2>&1; then
  drift "found retired registry '${retired_registry}'; public images must use '${image_repo}'"
fi

# 5. On a release tag build, the tag must match appVersion so the tagged commit's
#    tree truthfully reflects the version being published. The pipeline stamps the
#    artifacts from the tag regardless, but this stops a tag from shipping while
#    the checked-in baseline still names an older version. Only enforced when a
#    v* tag ref is present (RELEASE_REF, or Azure DevOps' BUILD_SOURCEBRANCH);
#    branch, PR, and local runs skip it.
release_ref="${RELEASE_REF:-${BUILD_SOURCEBRANCH:-}}"
case "${release_ref}" in
  refs/tags/v*)
    tag_version="$(printf '%s' "${release_ref#refs/tags/v}" | tr -d '\r')"
    if [ "${tag_version}" != "${app_version}" ]; then
      drift "release tag '${release_ref}' (version '${tag_version}') does not match appVersion '${app_version}'; run 'scripts/check-versions.sh --set-version ${tag_version}', commit, and re-tag that commit"
    else
      echo "Release tag ${release_ref} matches appVersion ${app_version}."
    fi
    ;;
esac

if [ "${fail}" -ne 0 ]; then
  echo "Version consistency check FAILED." >&2
  echo "Hint: run 'bash scripts/check-versions.sh --fix' to align everything to appVersion." >&2
  exit 1
fi
echo "Version consistency check passed; chart and manifests all pin ${app_version}."