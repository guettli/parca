# Dockerfile.ch — fork-only image for running upstream Parca with the
# ClickHouse storage backend (--clickhouse-enabled).
#
# Why this file exists at all: the stock release image is assembled by
# `Dockerfile` from binaries goreleaser has already cross-compiled, which is
# awkward to reproduce for a one-commit patch build. This does the full UI +
# Go build itself from the working tree, so an image can be cut from any branch
# of this fork.
#
# Unlike the retired Dockerfile.duckdb, this build is CGO-FREE: the ClickHouse
# backend talks over the network via clickhouse-go, so there is no cgo, no
# libduckdb, no glibc requirement, and the runtime base can be distroless
# static.
#
# IMPORTANT — drop-in compatibility: this image deliberately matches the stock
# upstream image on BOTH the entrypoint convention and the runtime uid (65534).
# Either one diverging breaks a swap: the wrong uid cannot read a PVC the stock
# image wrote (the metastore is mode 0700), and the wrong entrypoint convention
# changes what args[0] has to be.
#
# Entrypoint convention: this image deliberately sets NO ENTRYPOINT
# and CMD ["/parca"], exactly matching the stock upstream image. A Kubernetes
# container that supplies `args:` without `command:` replaces CMD and execs
# args[0], so the deployment must pass "/parca" as its first arg. Keeping this
# identical to upstream means swapping between the two images is a tag change
# and nothing else. Dockerfile.duckdb set ENTRYPOINT instead, which is why that
# image needed the opposite arg convention.

# ---------------------------------------------------------------------------
# 1. Build the web UI (embedded into the Go binary via ui/ui.go go:embed).
# ---------------------------------------------------------------------------
FROM docker.io/node:22.22.2-bookworm-slim AS ui-builder

# renovate: datasource=npm depName=pnpm versioning=npm
ARG PNPM_VERSION=10.33.0
RUN npm install --global "pnpm@${PNPM_VERSION}"

WORKDIR /app
# The pnpm workspace root is ui/ (ui/pnpm-workspace.yaml, ui/pnpm-lock.yaml).
COPY ui ./ui
WORKDIR /app/ui
RUN pnpm install --frozen-lockfile --prefer-offline && pnpm run build

# ---------------------------------------------------------------------------
# 2. Build the Parca binary. CGO_ENABLED=0 -> a static binary.
# ---------------------------------------------------------------------------
FROM docker.io/golang:1.26-bookworm AS go-builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY pkg ./pkg
COPY proto ./proto
COPY gen ./gen
COPY ui/ui.go ./ui/ui.go
COPY --from=ui-builder /app/ui/packages/app/web/build ./ui/packages/app/web/build

ARG VERSION=ch-dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags "-X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /parca ./cmd/parca

# Pre-create a writable data directory; distroless has no shell so we cannot
# mkdir at runtime. The ClickHouse backend still needs local storage for the
# debuginfo store and the badger metastore, which are built unconditionally
# regardless of backend.
#
# uid/gid 65534, NOT the distroless :nonroot default of 65532. The stock
# upstream image is alpine-based and runs `USER nobody`, which is 65534 there,
# so every file on an existing Parca PVC is owned by 65534 -- and the badger
# metastore directory is mode 0700, so a different uid cannot read it at all.
# Running as 65532 makes Parca die at startup with
#   failed to open badger database for metastore: open /var/lib/parca/metastore:
#   permission denied
# on any volume a stock image has already written. Matching the upstream uid is
# what makes this image a drop-in replacement.
RUN mkdir -p /data && touch /data/.keep && chown -R 65534:65534 /data

# ---------------------------------------------------------------------------
# 3. Runtime image — distroless static (no libc needed for a CGO-free binary).
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runner

LABEL \
    org.opencontainers.image.source="https://github.com/guettli/parca" \
    org.opencontainers.image.url="https://github.com/guettli/parca" \
    org.opencontainers.image.description="Parca continuous profiling — upstream build for the ClickHouse storage backend." \
    org.opencontainers.image.licenses="Apache-2.0"

COPY --from=go-builder --chown=65534:65534 /data /data
COPY --from=go-builder /parca /parca
COPY parca.yaml /parca.yaml

# Match the stock image: WORKDIR / so /parca.yaml and ./data resolve, and no
# ENTRYPOINT (see the note at the top of this file).
WORKDIR /
USER 65534:65534
EXPOSE 7070
CMD ["/parca"]
