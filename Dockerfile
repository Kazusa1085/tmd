# Multi-stage build: the go-sqlite3 driver needs cgo, so the builder carries a
# toolchain while the runtime image only needs the binary and its shared libs.
FROM golang:1.24-bookworm AS build

WORKDIR /src

# Dependencies first, so that editing source does not re-download modules.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=1 is required by mattn/go-sqlite3. -trimpath keeps the build
# reproducible and out of the binary's debug info.
RUN CGO_ENABLED=1 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w" \
        -o /out/tmd .

# debian-slim rather than alpine: the binary links against glibc, and Alpine
# would need a musl toolchain as well as extra packages here.
FROM debian:bookworm-slim

# ca-certificates is not optional: without it every HTTPS request to x.com
# fails. tzdata lets TZ actually take effect in the logs.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/tmd /usr/local/bin/tmd
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
# 0755 explicitly: the file copied in with 0711, and a non-root user needs read
# permission for `sh script` to work at all.
RUN chmod 0755 /usr/local/bin/tmd /usr/local/bin/entrypoint.sh

# The configuration directory (conf.yaml, targets.yaml, cookies, logs, reports)
# and the state directory (database, retry queue) are meant to be mounted. They
# are created here so that a bind mount onto an empty host directory still has
# the right ownership.
RUN mkdir -p /config /state /data \
    && useradd --uid 1000 --create-home --shell /usr/sbin/nologin tmd \
    && chown -R tmd:tmd /config /state /data

# The three mount points. TMD_ROOT_PATH and TMD_STATE_PATH are what make the
# mounts authoritative: if conf.yaml omits root_path or state_path, these win,
# so a mounted /data is where the media really goes instead of inside the
# container. A value in conf.yaml still takes precedence.
ENV TMD_CONFIG_DIR=/config \
    TMD_ROOT_PATH=/data \
    TMD_STATE_PATH=/state \
    TZ=UTC

WORKDIR /data

# Drop privileges by default. Writing to a bind mount requires the host
# directory to be writable by uid 1000, or `user:` to be set in compose.
USER tmd

# One-shot by design: the program downloads, reports, and exits. Schedule it
# from the NAS task scheduler or cron rather than keeping it resident.
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["--targets", "/config/targets.yaml"]
