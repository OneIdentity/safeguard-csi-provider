---
name: build-and-release
description: Use when building the binary or image, running or changing CI (Azure Pipelines), linting, the tools module, the chart/manifest version model, or cutting a release. Covers the exact commands, the tag→ghcr publish flow, and check-versions.sh.
---

# Build and Release

## Local commands

| Purpose | Command |
|---|---|
| Build all packages | `go build ./...` |
| Release binary (Linux static, version-stamped) | `make build` |
| Windows / macOS binary | `make build-windows` / `make build-darwin` |
| Container image (single arch) | `make container` |
| Multi-arch images | `make container-all` then `make push-manifest` |
| Race unit tests | `make unit-test` (CGO; needs gcc/clang) |
| Lint | `make lint` (builds pinned golangci-lint + misspell from `tools/`) |
| Tidy | `make mod` |
| Version consistency | `bash scripts/check-versions.sh` (`--fix` / `--set-version X.Y.Z`) |

`make build` stamps `pkg/version.{BuildDate,BuildVersion,Vcs}` via `-ldflags`.
The Dockerfile is `distroless/static` and only **copies** the prebuilt binary —
the Go build happens on the host/CI, not in the image build.

### Linting

`golangci-lint` is pinned in the separate `tools/` module (`tools/go.mod`,
currently v1.64.8) and **built from source** (by the Makefile locally, and by the
Azure lint step in CI) so it compiles with the active Go toolchain. This matters:
golangci-lint refuses to analyze a module whose `go` directive is newer than the
Go it was built with, so the **prebuilt release binaries (built with an older Go)
fail against this repo's go1.25 directive** — always build it from `tools/`, don't
download the release asset. There is **no `.golangci.yml`**, so the default linter
set runs — including `staticcheck` **SA1019**, which fails on deprecated APIs.
Keep `gofmt -l .` empty.

## CI

Azure Pipelines is the single CI system (same pattern as the other Safeguard SDK
repos — there is no GitHub Actions build/lint/test workflow).

- **Azure Pipelines** — `azure-pipelines.yml` + `pipeline-templates/go-ci-steps.yml`.
  The `Validate` job runs `go build`, `gofmt -l`, `go vet`, `golangci-lint`,
  `go test -race ./...`, and `check-versions.sh`. It runs on pushes to
  `main`/`release-*` and on PRs (via the `pr:` trigger), and the `Publish` job
  runs **only on a `v*` tag**. Validation reaches GitHub through the **Azure
  Pipelines GitHub App**, which posts a `safeguard-csi-provider (Build, lint, and
  test)` check.
- **CodeQL** — `.github/workflows/codeql.yml` (the only GitHub Actions workflow).

> **PR validation must be enabled on the pipeline in Azure DevOps** for the YAML
> `pr:` trigger to fire — the App being installed is not enough. If PRs show no
> `azure-pipelines` check (only CodeQL), the pipeline's *Pull request validation*
> trigger is off / overridden; enable it in the pipeline's Triggers settings. This
> is the gap that let broken changes merge before it was turned on.

`goVersion` is pinned in `pipeline-templates/global-variables.yml`; keep it ≥ the
`go` directive in `go.mod`. The golangci-lint version is pinned in `tools/go.mod`
(built from source), not in a pipeline variable.

## Release / publication

Publication is entirely tag-driven. There is **no manual image push** and nothing
in the tree is edited at release time.

1. `charts/safeguard-csi-provider/Chart.yaml` `appVersion` is the **single source
   of truth**. `version` must equal it, and every `deployment/` manifest pins
   `ghcr.io/oneidentity/safeguard-csi-provider:<appVersion>`.
2. Bump the baseline with `bash scripts/check-versions.sh --set-version X.Y.Z`
   (rewrites chart version/appVersion and aligns manifest image tags), then commit.
3. Push a `vX.Y.Z` tag. Azure Pipelines runs `Validate` (including
   `check-versions.sh` check #5: the tag version must equal `appVersion`, else the
   build fails), then `Publish`:
   - builds the multi-arch image and pushes it to
     `ghcr.io/oneidentity/safeguard-csi-provider`,
   - moves `:latest`,
   - cuts a GitHub Release.

`check-versions.sh` verifies: chart `version` == `appVersion`; chart image tags
empty or == `appVersion`; `deployment/` image tags == `appVersion`; the retired
`starlingdev.azurecr.io` registry is absent; and (tag builds only) tag ==
`appVersion`. Branch/PR/local runs skip the tag check.

## Dependency policy

Major-version bumps are excluded from Dependabot; patch/minor are grouped
(gomod root, gomod `/tools`, github-actions, docker). The `k8s.io` / `klog` /
gRPC stack must move as a coherent set — see `.agents/skills/dependencies`.
