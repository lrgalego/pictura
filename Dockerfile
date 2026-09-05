# syntax=docker/dockerfile:1

# --platform=$BUILDPLATFORM pins the builder to the *host* architecture and
# leaves Go to cross-compile via GOARCH. Without it, building a linux/amd64
# image on an arm64 Mac emulates the entire builder stage under QEMU — minutes
# instead of seconds.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build

WORKDIR /src

# Dependencies first: this layer is only invalidated when go.mod/go.sum change.
#
# htmx-ds (and any other private module) is fetched over SSH with the deploy
# machine's own agent, forwarded via BuildKit's ssh mount — shipyard deploy
# passes --ssh. The key never enters the image: the mount exists only for this
# RUN, and GOPRIVATE keeps Go from asking the public proxy for modules it will
# never have.
COPY go.mod go.sum ./
ENV GOPRIVATE=github.com/lrgalego
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends openssh-client git \
    && mkdir -p -m 0700 ~/.ssh && ssh-keyscan github.com >> ~/.ssh/known_hosts \
    && git config --global url."git@github.com:".insteadOf "https://github.com/"
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=ssh \
    go mod download

COPY . .

ARG TARGETARCH

# CGO_ENABLED=0 is what makes a distroless/static base possible at all: use
# pure-Go dependencies (modernc.org/sqlite, not mattn) so there is nothing to
# link against and the binaries are fully static.
#
# All binaries ship in one image so the same artifact serves HTTP, runs
# scheduled jobs, and handles admin tasks.
#
# No templ generate step: *_templ.go files are committed (`make generate`
# keeps them current), so the build needs only the Go toolchain.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    for target in "server:./cmd/server"; do \
        name="${target%%:*}"; pkg="${target#*:}"; \
        CGO_ENABLED=0 GOOS=linux GOARCH="$TARGETARCH" \
            go build -trimpath -ldflags='-s -w' -o "/out/$name" "$pkg"; \
    done

# distroless/static rather than scratch. The app needs three things scratch
# lacks, and this base supplies all of them for ~2MB:
#   - CA certificates, or every outbound HTTPS call fails
#   - /tmp, which modernc.org/sqlite uses for spills (large sorts, VACUUM)
#   - a non-root uid with a passwd entry
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/server /usr/local/bin/

# Mount a directory, never a bare database file: WAL keeps -wal and -shm
# sidecars next to the database, and a file-level mount would hide them.
VOLUME ["/data"]

EXPOSE 8080
USER nonroot:nonroot

# --host is the important one: servers default to 127.0.0.1, which would be
# unreachable from outside the container.
ENTRYPOINT ["/usr/local/bin/server"]
CMD ["--host", "0.0.0.0", "--port", "8080"]
