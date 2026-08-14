# Development

## Prerequisites

- Go 1.25+
- A C toolchain (gcc/clang) if you run the race detector locally
- Docker or `buildx` for container images
- `make`

## Project layout

```
cmd/                 Provider entrypoint and flag parsing
pkg/
  provider/          Safeguard credential retrieval (built on safeguard-go)
  server/            CSI provider gRPC server + health check
  utils/             gRPC helpers
  version/           Build-stamped version info
charts/              Helm chart
deployment/          Raw install manifests (Linux/Windows)
docs/                This documentation set
examples/            Sample SecretProviderClass and pod manifests
tools/               Pinned lint tooling (golangci-lint, misspell)
```

Nearly all Safeguard logic lives in `pkg/provider/provider.go`; it uses the
[`safeguard-go`](https://github.com/OneIdentity/safeguard-go) SDK's A2A context
for all appliance communication.

## Build

```bash
make build            # linux/amd64 -> _output/amd64/safeguard-csi-provider
make build-windows    # windows/amd64
make build-darwin     # darwin (local dev)
```

Override the target architecture with `ARCH`, e.g. `make build ARCH=arm64`.

Plain Go also works:

```bash
go build ./...
```

## Test

```bash
go test ./...         # fast, no C toolchain required
make unit-test        # race detector + coverage (needs CGO/gcc)
```

The provider tests are hermetic: they stand up an in-process TLS fake appliance
(`httptest`) and a generated client certificate, so no real Safeguard is needed.

> On Windows without gcc, `-race` (and therefore `make unit-test`) fails to link.
> Use `go test ./...` locally; run `-race` on Linux (with gcc) for the race detector.

## Lint

```bash
make lint             # golangci-lint + misspell over docs
```

## Container images

```bash
make container                 # single-arch local image
make container-linux           # buildx, linux/$(ARCH)
make container-windows         # buildx, windows nanoserver
make container-all             # all OS/arch combinations
```

Image coordinates are controlled by `REGISTRY_NAME`, `REPO_PREFIX`,
`IMAGE_NAME`, and `IMAGE_VERSION` in the `Makefile`.

## Continuous integration

CI is not configured in this repository yet. Before opening a pull request, run
the same checks locally:

```bash
go mod tidy && git diff --exit-code -- go.mod go.sum   # tidy check
go vet ./...
go test -race ./...                                    # Linux, needs gcc
```

## Dependencies

```bash
make mod              # go mod tidy
```

The provider tracks a released tag of `safeguard-go`. When bumping it, update
`go.mod`, run `make mod`, and re-run the tests.

## Release

1. Update [`CHANGELOG.md`](../CHANGELOG.md).
2. Bump `IMAGE_VERSION` in the `Makefile` (and chart `values.yaml` if pinned).
3. Tag the release and build/push images with `make container-all push-manifest`.

> **Planned:** release automation will move to a tag-triggered pipeline driven
> by [goreleaser](https://goreleaser.com/) — producing release artifacts
> (checksums, SBOM, changelog) and Linux `amd64`/`arm64` images plus a
> manifest. The Windows nanoserver multi-version images may continue to use
> `buildx` until the goreleaser manifest path is validated for them.
