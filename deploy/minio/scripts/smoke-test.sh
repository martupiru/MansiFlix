#!/bin/sh
set +x
set -eu
. /opt/minio/common.sh

hash_file() {
    # A pipeline to cut could hide sha256sum's failure in POSIX sh.
    checksum=$(sha256sum "$1") || fail 'SHA-256 computation failed'
    digest=${checksum%% *}
    [ "${#digest}" -eq 64 ] || fail 'invalid SHA-256 length'
    case "$digest" in
        *[!0-9a-f]*) fail 'invalid SHA-256 output' ;;
    esac
    printf '%s\n' "$digest"
}

load_credentials
wait_ready

: "${POD_UID:?POD_UID must come from the Kubernetes downward API}"
case "$POD_UID" in
    *[!a-zA-Z0-9-]*|'') fail 'invalid synthetic test identifier' ;;
esac
original="mansiflix-originals/_checks/$POD_UID/original.bin"
result="mansiflix-encoded/runs/_checks/$POD_UID/result.bin"
dd if=/dev/urandom of=/tmp/input.bin bs=1024 count=64 2>/dev/null
bytes=$(wc -c </tmp/input.bin) || fail 'synthetic size check failed'
[ "$bytes" -eq 65536 ] || fail 'unexpected synthetic file size'

# Administrator seeds ONLY a fresh synthetic object in the NEW MinIO store.
mc_ok pipe "admin/$original" </tmp/input.bin
if ! mc --json cat "worker/$original" >/tmp/worker-input.bin 2>/tmp/read-error; then
    fail 'worker could not download the known original'
fi
expected=$(hash_file /tmp/input.bin) || exit 1
worker_hash=$(hash_file /tmp/worker-input.bin) || exit 1
[ "$expected" = "$worker_hash" ] || fail 'original download SHA-256 mismatch'

# pipe streams a known key without listing or reading the result bucket.
mc_ok pipe "worker/$result" </tmp/worker-input.bin
if ! mc --json cat "worker/$result" >/tmp/download.bin 2>/tmp/read-error; then
    fail 'worker could not retrieve its synthetic round-trip object'
fi
actual=$(hash_file /tmp/download.bin) || exit 1
[ "$expected" = "$actual" ] || fail 'result round-trip SHA-256 mismatch'
printf 'PASS SHA-256: %s (%s bytes)\n' "$actual" "$bytes"

printf 'no-real-video\n' >/tmp/restricted.bin
restricted="mansiflix-encoded/runs/restricted/$POD_UID/result.bin"
mc_ok pipe "admin/$restricted" </tmp/restricted.bin
expect_denied read-processing-results cat "worker/$restricted"
expect_denied write-outside-runs pipe "worker/mansiflix-encoded/_outside/$POD_UID/result.bin" </tmp/input.bin
expect_denied list-originals ls worker/mansiflix-originals
expect_denied overwrite-original pipe "worker/$original" </tmp/input.bin
expect_denied delete-original rm --force "worker/$original"
expect_denied delete-result rm --force "worker/$result"
expect_denied administer-server admin info worker

# These are synthetic test objects only. If a permission is wrong, abort
# immediately. No real videos or legacy PVC are mounted or referenced.
if ! mc --json cat "admin/$original" >/tmp/original-after.bin 2>/tmp/read-error; then
    fail 'synthetic original disappeared during permission checks'
fi
after=$(hash_file /tmp/original-after.bin) || exit 1
[ "$expected" = "$after" ] || fail 'synthetic original changed'

if wget -S -T 3 -t 1 -O /tmp/anonymous-body "$MINIO_ENDPOINT/$original" 2>/tmp/anonymous-status; then
    fail 'anonymous download unexpectedly succeeded'
fi
grep -Eq 'HTTP/1\.[01] 403' /tmp/anonymous-status || fail 'anonymous request did not return HTTP 403'
printf 'PASS: anonymous access rejected; synthetic artifacts retained under _checks/%s/\n' "$POD_UID"
printf 'EVIDENCE original=%s result=%s restricted=%s sha256=%s\n' "$original" "$result" "$restricted" "$actual"
