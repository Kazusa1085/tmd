#!/bin/sh
# Entrypoint for the tmd container.
#
# Its only job is to fail early and clearly on the mistakes that are otherwise
# reported much later, as a confusing error about the retry queue or a database.

set -eu

CONFIG_DIR="${TMD_CONFIG_DIR:-/config}"

warn() { echo "tmd: $*" >&2; }

# `--help` must work without a mounted configuration, so that the image can be
# inspected before it is set up.
for arg in "$@"; do
    case "$arg" in
        -h|--help|-help)
            exec tmd "$@"
            ;;
    esac
done

# A missing conf.yaml turns into "failed to login" after prompting on stdin,
# which inside a container looks like a hang.
if [ ! -f "$CONFIG_DIR/conf.yaml" ]; then
    warn "no conf.yaml in $CONFIG_DIR"
    warn ""
    warn "Mount a configuration directory containing conf.yaml, for example:"
    warn "  -v /volume1/docker/tmd/config:$CONFIG_DIR"
    warn ""
    warn "Start from config/conf.example.yaml in the repository."
    exit 2
fi

if [ ! -w "$CONFIG_DIR" ]; then
    warn "$CONFIG_DIR is not writable (running as uid=$(id -u), gid=$(id -g))"
    warn "The program writes its logs and the target report there."
    warn "Fix the host directory ownership, or set 'user:' in docker compose."
    exit 2
fi

# State lives outside the media tree on purpose: SQLite needs real file locking.
STATE_DIR="${TMD_STATE_DIR:-}"
if [ -n "$STATE_DIR" ] && [ ! -w "$STATE_DIR" ]; then
    warn "$STATE_DIR is not writable (running as uid=$(id -u))"
    exit 2
fi

exec tmd "$@"
