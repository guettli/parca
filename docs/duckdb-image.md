# Single-node Parca image with the embedded DuckDB backend

The `duckdb-backend` branch adds an embedded [DuckDB](https://duckdb.org/)
storage backend (`--storage-backend=duckdb`), which keeps all profile data in a
single on-disk file and prunes by time window — so single-node query latency
scales with the query window rather than the total retained data.

This document describes how the container image for that backend is built and
the gotchas that make it different from the stock Parca release image.

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
on every push to `duckdb-backend` (and on manual `workflow_dispatch`). Pull
requests build and smoke-test the image but do not push it.

## Why a dedicated Dockerfile

The stock release image (`Dockerfile` + `.goreleaser.yml`) packages a
**CGO-free** binary that goreleaser cross-compiles for many OS/arch targets and
ships on an **alpine (musl)** base. That path does **not** work for DuckDB:

- **CGO is mandatory.** The backend depends on
  `github.com/marcboeker/go-duckdb/v2`, which links a static `libduckdb`
  through cgo. The build must run with `CGO_ENABLED=1` and a C/C++ toolchain
  (`.goreleaser.yml` pins `CGO_ENABLED=0`).
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
   + `pnpm run build` inside `ui/` (its own pnpm workspace root).
2. **go-builder** (`golang:1.26-bookworm`, has gcc/g++ + glibc): copies the
   built UI into `ui/packages/app/web/build`, then
   `CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build ./cmd/parca`. It also
   `go install`s `grpc_health_probe` for the container health check.
3. **runner** (`gcr.io/distroless/cc-debian12:nonroot`): copies the binary,
   `grpc_health_probe`, `parca.yaml` and a writable `/data` owned by the
   distroless nonroot user (uid/gid `65532` — distroless has no shell, so the
   directory is created and `chown`ed in the builder stage and copied in).

## Runtime notes

- The image `ENTRYPOINT` is `/parca`; the default `CMD` already selects the
  DuckDB backend (`--storage-backend=duckdb --duckdb-path=/data/parca.duckdb`).
  Extra flags passed to `docker run` are appended to the entrypoint.
- The DuckDB file path flag is `--duckdb-path` (there is no `--storage-path`
  flag). An empty `--duckdb-path` uses a volatile in-memory database.
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
