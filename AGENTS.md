# AGENTS.md — safeguard-csi-provider

A [Secrets Store CSI Driver](https://secrets-store-csi-driver.sigs.k8s.io/)
**provider** that fetches Safeguard for Privileged Passwords secrets over the
Application-to-Application (A2A) service and mounts them into Kubernetes pods.
Module path: `github.com/OneIdentity/safeguard-csi-provider`. It is built on the
[`safeguard-go`](https://github.com/OneIdentity/safeguard-go) SDK.

This project ships as a **container image**, not an importable library, so it
tracks a current Go toolchain and a current Kubernetes / gRPC dependency
generation — there is no consumer compatibility floor to preserve. Go floor:
1.25.

## What it is

The Secrets Store CSI Driver runs as a DaemonSet on every node. When a pod
mounts a `SecretProviderClass` that names `provider: safeguard`, the driver
calls this provider's gRPC `Mount` RPC over a Unix socket. The provider uses the
pod's client certificate (delivered as node-publish secrets) to authenticate to
Safeguard's A2A service, retrieves the requested credentials, and returns file
contents for the driver to write into the pod's mount.

```text
 kubelet ──▶ Secrets Store CSI Driver (DaemonSet) ──gRPC/unix──▶ this provider ──A2A/TLS──▶ Safeguard appliance
                                                                     │
                                                              safeguard-go SDK (A2AContext)
```

The provider implements the `sigs.k8s.io/secrets-store-csi-driver/provider/v1alpha1`
`CSIDriverProviderServer` (`Mount`, `Version`) plus the gRPC health service. The
`v1alpha1` wire contract is stable across CSI-driver releases, so the in-cluster
driver version and the imported module version are decoupled.

## Project structure

```text
.
├── cmd/                 # package main: flag parsing, gRPC server + health wiring, JSON logging
├── pkg/
│   ├── provider/        # the mount logic: A2A retrieval, output shaping (file-per-account vs bundle)
│   ├── server/          # CSIDriverProviderServer (Mount/Version), health server, healthz HTTP probe
│   ├── utils/           # endpoint parsing, gRPC logging interceptor
│   └── version/         # build-stamped version info (-ldflags)
├── charts/              # Helm chart (Chart.yaml appVersion is the version source of truth)
├── deployment/          # raw install manifests (image tags pinned to appVersion)
├── docs/                # installation.md, development.md, usage
├── scripts/             # check-versions.sh (chart/manifest/tag consistency gate)
├── test/                # live (-tags live) and e2e (-tags e2e) suites + shared harness
├── tools/               # separate module pinning golangci-lint + misspell
├── pipeline-templates/  # Azure Pipelines reusable steps/variables (the PR/CI gate)
├── .github/workflows/   # codeql.yml (CodeQL only; build/lint/test live in Azure)
├── azure-pipelines.yml  # validate on PR/branch; publish image + release only on v* tag
├── Dockerfile           # distroless/static runtime (binary is built on the host, then copied)
└── Makefile
```

## The mount request (provider contract)

`pkg/provider/provider.go` reads a `SecretProviderClass`'s `parameters` (the gRPC
`attributes`) and the node-publish `secrets`:

- **secrets** — `clientCertificate` / `clientKey` (PEM): the A2A client identity.
- **attributes** — `safeguardHost`, `appName` (optional A2A registration filter),
  `accountNames` (optional case-insensitive filter), `objectType`/`objectTypes`
  (`Password` | `PrivateKey` | `ApiKey`), `keyFormat` (`OpenSsh` | `Ssh2` |
  `Putty`), `outputFormat` (`file-per-account` (default) | `bundle`),
  `bundleFile` (default `secrets.json`), and appliance trust:
  `safeguardCaBundle` (PEM) or `insecureSkipVerify` (bootstrap only — logs loudly).

It enumerates retrievable accounts via `a2a.GetRetrievableAccounts`, then either
writes one file per account (named after the account) or one consolidated JSON
`bundle`. Key material is normalized to LF (Safeguard returns CRLF). Secrets flow
through the SDK's `Secret` type and are only `Expose`d at the moment of writing.

## Setup, build, lint, and test

| Purpose | Command |
|---|---|
| Build all packages | `go build ./...` |
| Unit tests | `go test ./...` (live/e2e are tag-gated, so this stays hermetic) |
| Race unit tests | `make unit-test` (or `go test -race ./...`) — needs a C toolchain |
| Vet | `go vet ./...` |
| Format check | CI runs `gofmt -l .`; use `gofmt -w` on edits |
| Lint | `make lint` (builds pinned golangci-lint from `tools/`, then misspell) |
| Version consistency | `bash scripts/check-versions.sh` |
| Release binary | `make build` (Linux static, `-ldflags` version stamp) |
| Container image | `make container` (or `container-all` for multi-arch) |

`golangci-lint` (pinned in `tools/go.mod`, currently v1.64.8) runs with **no repo
config**, so the default linters apply — including `staticcheck` **SA1019**, which
fails the build on deprecated-API use. Avoid deprecated gRPC/klog APIs.

Windows note: use `pwsh`. `-race` needs CGO + gcc/clang; the hosted CI (Linux)
runs it, so it is fine to skip `-race` locally and rely on CI.

## Testing

Unit tests use fakes and run by default. Two **gated** suites prove real behavior
against a **throwaway** Safeguard appliance (they create and delete users,
assets, accounts, certificates, and A2A registrations — never point them at a
production appliance). Full runbook, env vars, and WSL2/k3s networking are in
[`test/README.md`](test/README.md).

- **Live A2A** (`go test -tags live -v ./test/live/...`) — the light smoke test:
  a generated client certificate completes real password / SSH key / API key
  retrievals. Needs a reachable appliance + `SAFEGUARD_HOST` +
  `SAFEGUARD_BOOTSTRAP_PASSWORD` (and `SAFEGUARD_INSECURE=true` or
  `SAFEGUARD_CA_BUNDLE_FILE`).
- **End-to-end** (`sudo -E ... go test -tags e2e ...`) — the heavy test: drives a
  real `k3s` node, the CSI driver, and this provider's DaemonSet, then asserts the
  mounted file contents. Linux/WSL2 + root + `k3s`/`kubectl`/`helm`/`ko`. Run
  `sudo test/e2e/setup.sh` once first.

Live/e2e are the standard of proof for provider behavior; CI cannot run them (no
appliance is reachable), so run them locally against a lab appliance before a
release.

## Dependencies (the coupled Kubernetes / gRPC stack)

`k8s.io/*`, `sigs.k8s.io/secrets-store-csi-driver`, `k8s.io/klog/v2`,
`github.com/go-logr/*`, and `google.golang.org/grpc` must move as a **coherent
set** — an isolated bump breaks the build (e.g. klog's go-logr major must match
whatever provides the logger). Dependabot groups these patch/minor updates so
they land together; upgrade them deliberately and re-validate with a build + the
live/e2e suites. See `.agents/skills/dependencies`.

## CI/CD

- **PR/CI gate — Azure Pipelines** (`azure-pipelines.yml` +
  `pipeline-templates/go-ci-steps.yml`): `go build`, `gofmt`, `go vet`,
  `golangci-lint`, `go test -race`, and `check-versions.sh`, run on PRs and on
  pushes to `main`/`release-*` via the Azure Pipelines GitHub App. This is the
  build/lint/test gate (matches the other Safeguard SDK repos). PR validation
  must be enabled on the pipeline in Azure DevOps for the `pr:` trigger to fire.
- **CodeQL** (`.github/workflows/codeql.yml`): Go + Actions analysis (the only
  GitHub Actions workflow — build/lint/test is not duplicated here).
- **Publish — Azure Pipelines**: a `v*` tag builds the multi-arch image, pushes
  it to `ghcr.io/oneidentity/safeguard-csi-provider`, moves `:latest`, and cuts a
  GitHub Release. Nothing in the tree is edited to release — the tag stamps
  everything. `check-versions.sh` (check #5) fails a tag build whose version does
  not equal `Chart.yaml` `appVersion`. See `.agents/skills/build-and-release`.

## Coding conventions

- Context-first provider APIs; keep the public gRPC contract (`v1alpha1`) intact.
- Route all secret material through the SDK `Secret` type; only `Expose`/
  `ExposeString` at the write boundary. Never log, serialize, or commit
  credentials, client keys, or API keys.
- `insecureSkipVerify` / `WithInsecureTLS` is bootstrap-only and must stay loud.
- No deprecated APIs (SA1019 fails lint). Keep `gofmt` clean.
- Stamp version info via `-ldflags` (`pkg/version`), never hard-code.

## Security

- Never commit passwords, tokens, A2A API keys, private keys, or generated test
  certs. Prefer `safeguardCaBundle` trust over `insecureSkipVerify`.
- GitHub Private Vulnerability Reporting is enabled in the Security tab (see
  `SECURITY.md`); Dependabot + CodeQL are configured.

## Commit and PR workflow

The maintainer reviews and approves every commit message before commits are
created. Do not commit without explicit instruction. Stage specific files, never
`-A`. Open PRs from a fork into `OneIdentity/safeguard-csi-provider`; `main` is
protected (PR + review + required CI check). PRs describe the behavior they
change and which tests prove it.

## On-demand skills

| Skill | When to read | File |
|---|---|---|
| Architecture | Provider/server/health wiring, the mount flow, safeguard-go A2A integration | `.agents/skills/architecture/SKILL.md` |
| Build and Release | Makefile, Azure Pipelines, `check-versions.sh`, tag→ghcr publish, versioning | `.agents/skills/build-and-release/SKILL.md` |
| Testing Guide | Unit, live (`-tags live`), and e2e (`-tags e2e`) suites; env vars; WSL2/k3s | `.agents/skills/testing-guide/SKILL.md` |
| Dependencies | The coupled k8s/gRPC/klog stack, coherent-set upgrades, Dependabot grouping | `.agents/skills/dependencies/SKILL.md` |

## Keeping this file current

When a change affects build, lint, testing, publication, the dependency stack, or
skill routing, update this file and the relevant `.agents/skills/*/SKILL.md` in
the same change. Keep this file short — move deeper material into skills.
