# Single-node Parca image with the embedded DuckDB backend

This fork replaces Parca's FrostDB storage backend with an embedded
[DuckDB](https://duckdb.org/) backend (`--storage-backend=duckdb`), which keeps
all profile data in a single on-disk file and prunes by time window — so
single-node query latency scales with the query window rather than the total
retained data. It also adds in-process time-based retention
(`--duckdb-retention`) so the file stays bounded.

This document describes how the container image for that backend is built and
the gotchas that make it different from the stock Parca release image.

## This fork's `main` is the project

**Work on `main`.** This fork's `main` *is* the DuckDB build — it is not a
branch staged for an upstream PR, and it does not track upstream unchanged.
Build, deploy, and base new work on `main`; the deployed image
`ghcr.io/guettli/parca:duckdb` (pinned by the `guettli/gitops` Parca Deployment)
is the one built from `main` HEAD.

## TL;DR

```console
docker pull ghcr.io/guettli/parca:duckdb
docker run --rm -p 7070:7070 -v parca-data:/data \
  ghcr.io/guettli/parca:duckdb \
  --storage-backend=duckdb --duckdb-path=/data/parca.duckdb
# open http://localhost:7070/
```

The image is built by [`Dockerfile.duckdb`](../Dockerfile.duckdb) and published
by [`.github/workflows/duckdb-image.yml`](../.github/workflows/duckdb-image.yml)
on every push to `main` (and on manual `workflow_dispatch`). Pull
requests build and smoke-test the image but do not push it.

## Why a dedicated Dockerfile

The stock release image (`Dockerfile` + `.goreleaser.yml`) packages a
**CGO-free** binary that goreleaser cross-compiles for many OS/arch targets and
ships on an **alpine (musl)** base. That path does **not** work for DuckDB:

- **CGO is mandatory.** The backend depends on
  `github.com/marcboeker/go-duckdb/v2`, which links a static `libduckdb`
  through cgo. The build must run with `CGO_ENABLED=1` and a C/C++ toolchain
  (`.goreleaser.yml` pins `CGO_ENABLED=0`). Because go-duckdb cannot be
  cross-compiled CGO-free, the backend is guarded by the `duckdb` build tag: it
  is only compiled into `cmd/parca` when built with `-tags duckdb`. The default
  (release) build omits the tag and uses a stub that returns an error if
  `--storage-backend=duckdb` is selected, keeping the CGO-free cross-compiled
  goreleaser build working.
- **glibc, not musl.** The prebuilt `libduckdb` static libraries
  (`github.com/duckdb/duckdb-go-bindings/linux-amd64`) are compiled against
  glibc/libstdc++. The resulting binary links `libstdc++`, `libgcc_s` and
  `libc` dynamically and therefore **cannot run on alpine**. The runtime base
  must be glibc-based — we use `gcr.io/distroless/cc-debian12`, which ships
  glibc, `libstdc++` and `libgcc`.
- **Cross-compilation is impractical.** CGO cross-compilation would need a
  cross C/C++ toolchain per target, so this image targets **`linux/amd64`
  only** (the single-node deployment target). arm64/darwin/windows are out of
  scope here.
- **The web UI must be built first.** The UI is embedded into the binary via
  `ui/ui.go` (`//go:embed packages/app/web/build`), so `pnpm run build` (i.e.
  `make ui/build`) has to run before the Go build or the binary embeds an empty
  UI.

`Dockerfile.duckdb` therefore performs the whole build itself in three stages:

1. **ui-builder** (`node:22-bookworm-slim`): `pnpm install --frozen-lockfile`
   - `pnpm run build` inside `ui/` (its own pnpm workspace root).
2. **go-builder** (`golang:1.26-bookworm`, has gcc/g++ + glibc): copies the
   built UI into `ui/packages/app/web/build`, then
   `CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags duckdb ./cmd/parca`
   (the `duckdb` tag pulls in the cgo backend). It also
   `go install`s `grpc_health_probe` for the container health check.
3. **runner** (`gcr.io/distroless/cc-debian12:nonroot`): copies the binary,
   `grpc_health_probe`, `parca.yaml` and a writable `/data` owned by the
   distroless nonroot user (uid/gid `65532` — distroless has no shell, so the
   directory is created and `chown`ed in the builder stage and copied in).

## Runtime notes

- The image `ENTRYPOINT` is `/parca`; the default `CMD` already selects the
  DuckDB backend (`--storage-backend=duckdb --duckdb-path=/data/parca.duckdb`).
  Extra flags passed to `docker run` are appended to the entrypoint.
  - **Kubernetes gotcha:** because the entrypoint is `/parca`, a pod's `args:`
    must be **flags only** — do NOT start them with `/parca`. The stock
    `parca-dev/parca` image had no such entrypoint, so manifests copied from it
    often lead with `- /parca`; here that becomes a positional arg and the pod
    crash-loops with `parca: error: unexpected argument /parca`.
- The DuckDB file path flag is `--duckdb-path` (there is no `--storage-path`
  flag). An empty `--duckdb-path` uses a volatile in-memory database.
- **Memory:** DuckDB manages its own (C++) allocations and runs at its default
  `memory_limit` (a fraction of *detected* host RAM), which a container may see
  as the whole node, not the cgroup limit. `GOMEMLIMIT` only bounds the Go heap,
  not DuckDB. Time-based retention plus time-pruned queries keep working sets
  small, but under a heavy query the pod can exceed its memory limit and be
  OOMKilled; if that happens, wire a DuckDB `memory_limit` into the backend
  rather than only raising the limit.
- `parca.yaml` still configures object storage (used for debuginfo/symbols); it
  defaults to the filesystem bucket under `./data`, which resolves to `/data`
  in the image. Mount a volume at `/data` to persist both the DuckDB file and
  object storage.
- HTTP (and multiplexed gRPC) listen on `:7070`.

## Smoke test

The CI workflow runs the container with a config that scrapes Parca's own
`/debug/pprof` endpoints, then asserts that:

1. the HTTP server comes up on `:7070`,
2. the logs contain `initializing DuckDB storage backend` (proving the cgo
   DuckDB driver loaded and the flag took effect),
3. the embedded UI is served at `/`,
4. the query API answers — `GET /api/profiles/types` returns JSON, and
5. profile data becomes queryable end to end — after a few self-scrapes,
   `GET /api/profiles/types` reports a non-empty list of profile types.

The JSON/REST API is served by grpc-gateway under `/api` (the native gRPC
service is multiplexed on the same `:7070` port over HTTP/2). Requests to an
unknown path fall through to the single-page UI, so the smoke test parses the
response as JSON to make sure it actually reached the query service rather than
the SPA.
