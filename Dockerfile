# Custom CLIProxyAPI image with the quota and auth plugins baked in.
#
# Why this exists: CPA loads plugins from /CLIProxyAPI/plugins/linux/amd64/,
# which in the live deployment is the container's *writable layer*, not
# persistent storage. Anything installed there with `kubectl cp` is lost when
# the container is replaced, and nothing reinstalls it at startup (the store
# reconciliation path is gated behind `configLoadedFromHome`, which this
# instance does not use). Baking the artifacts into the image is what makes
# them durable. See issue #10.
#
# The builder is deliberately bookworm-based, matching the runtime image. The
# plugins are cgo `c-shared` libraries, so they link against glibc; building on
# the same distro as the runtime removes the libc-compatibility question
# entirely rather than reasoning about it from `objdump -T` output.

# --- build stage -------------------------------------------------------------
FROM docker.io/library/golang:1.24-bookworm AS build

WORKDIR /src

# Dependencies first, so the module cache layer survives source edits.
COPY go.mod go.sum ./
RUN go mod download

# Only what the plugins need. The upstream/ submodule is deliberately excluded:
# the ABI types are hand-copied into this repo, so the build does not import
# upstream at all. It is used by `make verify-upstream`, which runs in CI as a
# separate step.
COPY *.go ./
COPY authplugin/ ./authplugin/

# CGO_ENABLED=1 is required twice over: buildmode=c-shared needs it, and the
# go-sqlite3 dependency is cgo-based.
ENV CGO_ENABLED=1
RUN go build -buildmode=c-shared -trimpath -ldflags='-s -w' \
      -o /out/cpa-quota-api-extension.so . \
 && go build -buildmode=c-shared -trimpath -ldflags='-s -w' \
      -o /out/cpa-opencode-go-auth.so ./authplugin \
 && rm -f /out/*.h

# --- runtime stage -----------------------------------------------------------
# Pinned by digest to match k8s-conf/apps/cliproxyapi/release.yaml. Bump both
# together; Renovate manages the digest in k8s-conf.
FROM docker.io/eceasy/cli-proxy-api:v7.2.137@sha256:591a09c19de769be09a2e56277365cd568b83fc7d98c94d2e7e7bef7069f7422

# Path is relative to CPA's workdir (/CLIProxyAPI) as resolved from
# `plugins.dir` in config.yaml.
COPY --from=build /out/*.so /CLIProxyAPI/plugins/linux/amd64/

# Inherited from the base image, restated so a base-image change that drops
# them is caught here rather than at deploy time. The image has no ENTRYPOINT,
# and k8s-conf sets `command` explicitly for the same reason.
CMD ["/CLIProxyAPI/CLIProxyAPI"]
