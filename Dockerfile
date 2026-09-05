# Custom CLIProxyAPI image with the quota, auth, and session-cache plugins
# baked in.
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

# --- third-party plugin stage ------------------------------------------------
# The GitHub Copilot *provider* plugin. Not built here -- fetched from its
# upstream release. It is baked in because it suffers the same ephemerality as
# our own plugins: it vanished from the live instance when the pod was replaced
# on 2026-08-30, taking Copilot model serving with it. See issue #11.
#
# Pinned by version *and* checksum. The checksum is the one published in the
# release's checksums.txt, re-verified here so a re-tagged or replaced asset
# fails the build rather than shipping silently.
FROM docker.io/library/golang:1.24-bookworm AS thirdparty

ARG COPILOT_VERSION=0.3.3
ARG COPILOT_SHA256=6ac5c58fe2507468d08bc33d6365cfb822180ba3f37ae51e06cea884b933ca3a

RUN apt-get update \
 && apt-get install -y --no-install-recommends unzip \
 && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    url="https://github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/releases/download/v${COPILOT_VERSION}/cliproxyapi-copilot_${COPILOT_VERSION}_linux_amd64.zip"; \
    curl -fsSL -o /tmp/copilot.zip "$url"; \
    echo "${COPILOT_SHA256}  /tmp/copilot.zip" | sha256sum -c -; \
    mkdir -p /out; \
    unzip -j /tmp/copilot.zip 'cliproxyapi-copilot.so' -d /out; \
    test -f /out/cliproxyapi-copilot.so

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
COPY sessioncache/ ./sessioncache/

# CGO_ENABLED=1 is required twice over: buildmode=c-shared needs it, and the
# go-sqlite3 dependency is cgo-based.
ENV CGO_ENABLED=1
RUN go build -buildmode=c-shared -trimpath -ldflags='-s -w' \
      -o /out/cpa-quota-api-extension.so . \
 && go build -buildmode=c-shared -trimpath -ldflags='-s -w' \
      -o /out/cpa-opencode-go-auth.so ./authplugin \
 && go build -buildmode=c-shared -trimpath -ldflags='-s -w' \
      -o /out/cpa-session-cache.so ./sessioncache \
 && rm -f /out/*.h

# --- runtime stage -----------------------------------------------------------
# Pinned by digest, tracking whatever k8s-conf/apps/cliproxyapi/release.yaml
# runs. Renovate bumps that repo; this pin must be bumped in step or the image
# will ship an older CPA than the cluster expects.
FROM docker.io/eceasy/cli-proxy-api:v7.2.146@sha256:238691ac26ce55e4d1c5219d72e3ad74838f81eda26359912eeb415e2820d163

# Plugin discovery scans <plugins.dir>/<goos>/<goarch> first, falling back to
# <plugins.dir> (internal/pluginhost/platform.go:308-312). plugins.dir is
# resolved relative to the workdir, /CLIProxyAPI.
#
# Filenames are deliberately UNVERSIONED. Discovery parses a `-v<version>`
# suffix from the filename, and if the config carries a `store.version` for
# that plugin ID, any file whose parsed version differs is **silently skipped**
# (platform.go:166-174) -- the pod comes up healthy with no plugin loaded. So
# the config must carry no `store:` block for these IDs, and in exchange an
# image bump needs no config change at all.
COPY --from=build /out/*.so /CLIProxyAPI/plugins/linux/amd64/
COPY --from=thirdparty /out/cliproxyapi-copilot.so /CLIProxyAPI/plugins/linux/amd64/

# Inherited from the base image, restated so a base-image change that drops
# them is caught here rather than at deploy time. The image has no ENTRYPOINT,
# and k8s-conf sets `command` explicitly for the same reason.
CMD ["/CLIProxyAPI/CLIProxyAPI"]
