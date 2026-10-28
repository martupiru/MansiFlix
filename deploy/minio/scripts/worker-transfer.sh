#!/bin/sh
# S3 adapter for the existing encoder. This script never loads root credentials.
set +x
set -eu
umask 077
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
: "${WORKER_ACCESS_KEY:?worker Secret required}"
: "${WORKER_SECRET_KEY:?worker Secret required}"
: "${POD_UID:?downward API identity required}"
case "$POD_UID" in ''|*[!a-zA-Z0-9-]*) fail 'invalid run identity';; esac
[ "$WORKER_ACCESS_KEY" = mansiflix-worker ] || fail 'unexpected worker identity'
case "$WORKER_SECRET_KEY" in ''|*[!A-Za-z0-9_-]*) fail 'invalid credential encoding';; esac
export MC_CONFIG_DIR=/tmp/mc MC_NO_COLOR=1
export MC_HOST_worker="http://${WORKER_ACCESS_KEY}:${WORKER_SECRET_KEY}@minio:9000"
prefix="mansiflix-encoded/runs/hls-demo/$POD_UID/360"
case "${1:-}" in
    download)
        if ! mc cat worker/mansiflix-originals/demo/synthetic.mp4 >/work/input.mp4 2>/tmp/mc-error; then
            fail 'S3 original download failed'
        fi
        [ -s /work/input.mp4 ] || fail 'empty S3 original'
        sha256sum /work/input.mp4 >/work/input.sha256
        printf 'PASS: synthetic input downloaded\n'
        ;;
    upload)
        [ -f /work/hls/index.m3u8 ] && [ ! -L /work/hls/index.m3u8 ] &&
            [ -s /work/hls/index.m3u8 ] || fail 'missing HLS playlist'
        # Parse the supported flat MPEG-TS VOD format, preserving playlist order.
        LC_ALL=C awk '
            { sub(/\r$/, "") }
            NR == 1 { if ($0 != "#EXTM3U") exit 1; next }
            /^[ \t]*$/ { next }
            ended { exit 1 }
            $0 == "#EXT-X-ENDLIST" { ended = 1; next }
            /^#/ { next }
            {
                if ($0 !~ /^segment_[0-9]+\.ts$/ || seen[$0]++) exit 1
                print; count++
            }
            END { if (!ended || !count) exit 1 }
        ' /work/hls/index.m3u8 >/work/segments || fail 'invalid or incomplete HLS playlist'
        while IFS= read -r name; do
            [ -f "/work/hls/$name" ] && [ ! -L "/work/hls/$name" ] &&
                [ -s "/work/hls/$name" ] || fail 'missing, unsafe or empty HLS segment'
        done </work/segments
        # Reject orphan files as well as missing references, before any S3 write.
        : >/work/local-segments
        for segment in /work/hls/segment_*.ts; do
            printf '%s\n' "${segment##*/}" >>/work/local-segments
        done
        LC_ALL=C sort /work/segments >/work/expected-segments
        LC_ALL=C sort /work/local-segments >/work/actual-segments
        cmp -s /work/expected-segments /work/actual-segments || fail 'orphan HLS segments'
        # Compute every checksum before the first remote write.
        (
            cd /work/hls
            while IFS= read -r name; do
                sha256sum "$name" || exit 1
            done </work/segments
            sha256sum index.m3u8
        ) >/work/checksums.sha256 || fail 'HLS checksum computation failed'
        count=0
        while IFS= read -r name; do
            if ! mc pipe --part-size 5MiB --concurrent 1 "worker/$prefix/$name" <"/work/hls/$name" >/tmp/mc-response 2>&1; then
                fail 'S3 segment upload failed'
            fi
            count=$((count + 1))
        done </work/segments
        if ! mc pipe --part-size 5MiB --concurrent 1 "worker/$prefix/checksums.sha256" </work/checksums.sha256 >/tmp/mc-response 2>&1; then
            fail 'S3 checksums upload failed'
        fi
        # Playlist publication is last; success implies all segments were uploaded.
        if ! mc pipe --part-size 5MiB --concurrent 1 "worker/$prefix/index.m3u8" </work/hls/index.m3u8 >/tmp/mc-response 2>&1; then
            fail 'S3 playlist upload failed'
        fi
        printf 'PASS HLS upload: %s/index.m3u8 segments=%s\n' "$prefix" "$count"
        ;;
    *) fail 'expected download or upload';;
esac
