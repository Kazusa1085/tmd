#!/bin/sh
# Entrypoint for the tmd container.
#
# Its job is to fail early and clearly on the mistakes that are otherwise
# reported much later, or not at all -- and one of them is silent data loss:
# a container that is not actually given the media volume writes the downloaded
# files inside itself, and they vanish with the container.

set -eu

CONFIG_DIR="${TMD_CONFIG_DIR:-/config}"
MEDIA_DIR="${TMD_ROOT_PATH:-/data}"
STATE_DIR="${TMD_STATE_PATH:-/state}"

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
    warn "The host directory must be writable by that uid: either chown it, or"
    warn "run the container as your own uid with -u \$(id -u):\$(id -g)."
    exit 2
fi

# The media directory is where the downloaded files go, and it is the whole
# point of running this. Verify it is a real mount, not the container's own
# filesystem: writing there would look like success and then lose everything.
is_mountpoint() {
    [ -d "$1" ] || return 1
    [ "$(stat -c %d "$1" 2>/dev/null)" != "$(stat -c %d / 2>/dev/null)" ]
}

if ! is_mountpoint "$MEDIA_DIR"; then
    warn "$MEDIA_DIR is not a mounted volume."
    warn ""
    warn "Without it the downloaded media is written inside the container and is"
    warn "destroyed when the container exits. Mount your media directory:"
    warn "  -v /volume1/media/tmd:$MEDIA_DIR"
    exit 2
fi

if [ ! -w "$MEDIA_DIR" ]; then
    warn "$MEDIA_DIR is not writable (running as uid=$(id -u), gid=$(id -g))"
    warn "Either chown the host directory to that uid, or run with"
    warn "  -u \$(id -u):\$(id -g)"
    exit 2
fi

# State holds the database and the retry queue. It should be a separate local
# volume: SQLite needs reliable file locking, which network shares do not have.
if [ ! -w "$STATE_DIR" ]; then
    warn "$STATE_DIR is not writable (running as uid=$(id -u))"
    warn "Point state_path at a writable local volume, or mount one there."
    exit 2
fi

if ! is_mountpoint "$STATE_DIR"; then
    warn "$STATE_DIR is not a mounted volume; the database will live inside the"
    warn "container and the timeline watermarks will be lost on exit, which makes"
    warn "every run download everything again."
fi

exec tmd "$@"
