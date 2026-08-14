---
name: dependencies
description: Use when bumping or reasoning about the coupled Kubernetes / gRPC / klog / go-logr dependency stack, resolving a build break from an isolated bump, choosing target versions, or configuring Dependabot for this repo. Explains why these deps must move as a coherent set and how the JSON-logging and gRPC seams break.
---

# Dependencies

This is a **container image**, not an importable library, so there is no consumer
Go compatibility floor to preserve. Track a current Go toolchain and a current
Kubernetes / gRPC generation. Go floor lives in `go.mod`, mirrored by
`pipeline-templates/global-variables.yml` (`goVersion`), `.github/workflows/ci.yml`
(`GO_VERSION`), and `codeql.yml` (`go-version`) — change them together.

## The coupled set (must move together)

- `sigs.k8s.io/secrets-store-csi-driver` — the provider gRPC contract
  (`provider/v1alpha1`, stable across releases; only the generated stubs are
  imported, so this is decoupled from the in-cluster driver version).
- `k8s.io/klog/v2` — logging.
- `github.com/go-logr/logr` (+ `zapr`) — klog's logger backend.
- `google.golang.org/grpc` — the server/health/interceptor APIs.
- Formerly `k8s.io/component-base` — only ever used for one JSON logger; now
  dropped in favor of a direct `zapr`+`zap` logger.

An **isolated** bump of any one of these tends to break the build, because the
`logr` major version they agree on is a cross-cutting contract:

- **The klog/go-logr trap** (the real 2024 break): bumping `klog/v2` alone pulled
  `go-logr v1` (struct `logr.Logger`), which was incompatible with the pinned
  ancient `component-base v0.22.0` (interface `logr.Logger` v0.4 API) →
  `cannot use ... as logr.Logger value` in `cmd/main.go`. The fix was to move the
  whole stack forward together, not to pin klog back.

## Known upgrade seams (code that breaks)

1. **JSON logging** (`cmd/main.go`). Old code used
   `k8s.io/component-base/logs/json.JSONLogger`, a variable that upstream
   removed. Current code builds the logger directly:
   `klog.SetLogger(zapr.NewLogger(zap.NewProduction()))`. Don't reintroduce
   `component-base/logs/json`.
2. **gRPC deprecations** (`pkg/server/healthz.go`). `grpc.Dial` and
   `grpc.WithInsecure` are staticcheck-deprecated (SA1019) and fail lint. Use
   `grpc.NewClient("passthrough:///"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(...))`.
3. **gRPC health service** (`pkg/server/server.go`). The health interface gained
   `List` and uses the mustEmbed forward-compat pattern. Embed
   `grpc_health_v1.UnimplementedHealthServer` in the server struct; explicit
   `Check`/`Watch` still override the defaults.
4. **Stricter `go vet`** after a Go bump can surface latent issues (e.g. a
   non-constant `fmt.Printf` format). Fix them in the same change.

## Upgrade procedure

1. `go get` the coupled set to a coherent generation (pick the CSI driver
   version, let klog/go-logr/grpc/x-crypto follow), then `go mod tidy`.
2. `go build ./...` and fix the seams above.
3. `go vet ./...`, `gofmt`, `make lint` (SA1019 will catch stragglers), and
   `go test -race ./...`.
4. Compile-check the tagged suites: `go vet -tags live ./test/live/... ./test/harness/...`
   and `go vet -tags e2e ./test/e2e/...`.
5. Update every Go-version pin (see above) if the floor moved.
6. Re-validate against a lab appliance: **live** always, **e2e** when the CSI
   driver generation changed (see `.agents/skills/testing-guide`).

## Dependabot config (`.github/dependabot.yml`)

- Four ecosystems: gomod root, gomod `/tools`, github-actions, docker.
- Major-version bumps are ignored (human-reviewed).
- Patch+minor are **grouped** per ecosystem, so the k8s/klog/gRPC updates arrive
  as one PR and stay mutually compatible instead of the isolated bump that broke
  the build. Do not re-add single-dependency pins to "hold a floor" — the group
  is what keeps the stack coherent.
