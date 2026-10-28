#!/bin/sh
# Administrative verification/cleanup, never a normal encoder entrypoint.
set +x
set -eu
. /opt/minio/common.sh
load_credentials
wait_ready
: "${CHECK_UID:?explicit synthetic test identity required}"
case "$CHECK_UID" in ''|*[!a-zA-Z0-9-]*) fail 'invalid test identity';; esac
case "${TEST_MODE:-}" in
    persistence)
        : "${EXPECTED_SHA256:?previously recorded checksum required}"
        [ "${#EXPECTED_SHA256}" -eq 64 ] || fail 'invalid expected checksum'
        case "$EXPECTED_SHA256" in *[!0-9a-f]*) fail 'invalid expected checksum';; esac
        if ! mc cat "worker/mansiflix-encoded/runs/_checks/$CHECK_UID/result.bin" >/work/result.bin 2>/tmp/read-error; then
            fail 'persisted object could not be downloaded'
        fi
        checksum=$(sha256sum /work/result.bin) || fail 'checksum failed'
        [ "${checksum%% *}" = "$EXPECTED_SHA256" ] || fail 'persisted object changed'
        printf 'PASS persistence: same object SHA-256=%s\n' "$EXPECTED_SHA256"
        ;;
    hls)
        prefix="admin/mansiflix-encoded/runs/hls-demo/$CHECK_UID/360"
        mkdir -p /work/hls
        for name in index.m3u8 checksums.sha256; do
            if ! mc cat "$prefix/$name" >"/work/hls/$name" 2>/tmp/read-error; then
                fail 'HLS verification download failed'
            fi
        done
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
        # Parse fields explicitly and normalize separators for sha256sum -c.
        # Reject duplicate names, paths, extra fields and malformed digests.
        LC_ALL=C awk '
            {
                sub(/\r$/, "")
                if (NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-fA-F]/ ||
                    $2 !~ /^(segment_[0-9]+\.ts|index\.m3u8)$/ || seen[$2]++) exit 1
                print tolower($1) "  " $2
                count++
            }
            END { if (!count) exit 1 }
        ' /work/hls/checksums.sha256 >/work/validated-checksums || fail 'invalid checksum manifest'
        {
            printf 'index.m3u8\n'
            cat /work/segments
        } >/work/expected-files
        LC_ALL=C sort /work/expected-files >/work/expected-sorted
        awk '{ print $2 }' /work/validated-checksums >/work/hashed-files
        LC_ALL=C sort /work/hashed-files >/work/hashed-sorted
        cmp -s /work/expected-sorted /work/hashed-sorted || fail 'checksum coverage mismatch'
        count=0
        while IFS= read -r name; do
            printf '%s\n' "$name" | grep -Eq '^segment_[0-9]+\.ts$' || fail 'unexpected segment path'
            if ! mc cat "$prefix/$name" >"/work/hls/$name" 2>/tmp/read-error; then
                fail 'HLS segment download failed'
            fi
            [ -s "/work/hls/$name" ] || fail 'empty HLS segment'
            count=$((count + 1))
        done </work/segments
        [ "$count" -gt 0 ] || fail 'no HLS segments'
        (cd /work/hls && sha256sum -c /work/validated-checksums) || fail 'HLS checksum mismatch'
        printf 'PASS HLS verification: segments=%s prefix=%s\n' "$count" "${prefix#admin/}"
        ;;
    cleanup)
        # Exact keys from smoke-test.sh only; never recursive bucket deletion.
        for key in "mansiflix-originals/_checks/$CHECK_UID/original.bin" \
                   "mansiflix-encoded/runs/_checks/$CHECK_UID/result.bin" \
                   "mansiflix-encoded/runs/restricted/$CHECK_UID/result.bin"; do
            mc_ok rm --force "admin/$key"
        done
        printf 'PASS: exact smoke-test objects removed\n'
        ;;
    *) fail 'expected persistence, hls or cleanup';;
esac
