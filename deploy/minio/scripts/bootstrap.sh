#!/bin/sh
set +x
set -eu
. /opt/minio/common.sh
load_credentials
wait_ready

# An existing identity is never rotated. Its Secret is verified below by a real
# authenticated download, so retries can complete a partially configured store.
create_worker=true
if mc --json admin user info admin mansiflix-worker >/tmp/user-check.json 2>&1; then
    create_worker=false
elif ! grep -Eq '"Code"[[:space:]]*:[[:space:]]*"(XMinioAdminNoSuchUser|NoSuchUser)"' /tmp/user-check.json; then
    fail 'cannot safely establish that the worker identity is absent'
fi

mc_ok mb --ignore-existing admin/mansiflix-originals
mc_ok mb --ignore-existing admin/mansiflix-encoded
mc_ok anonymous set none admin/mansiflix-originals
mc_ok anonymous set none admin/mansiflix-encoded
mc_ok admin policy create admin mansiflix-worker /opt/minio/worker.json

# This pinned mc version accepts both user keys on stdin. Do not use --json
# here: its JSON success response includes the newly created secret key.
if [ "$create_worker" = true ]; then
    if ! printf '%s\n%s\n' "$worker_user" "$worker_password" |
        mc admin user add admin >/tmp/user-create-response 2>&1; then
        fail 'worker identity creation failed; no credentials were logged'
    fi
fi
mc_ok admin policy attach admin mansiflix-worker --user mansiflix-worker
printf 'mansiflix-bootstrap-identity-check\n' >/tmp/identity-check
mc_ok pipe admin/mansiflix-originals/_checks/bootstrap/identity.bin </tmp/identity-check
if ! mc cat worker/mansiflix-originals/_checks/bootstrap/identity.bin >/tmp/identity-downloaded 2>/tmp/read-error; then
    fail 'worker Secret did not authenticate with its configured policy; no credentials were changed'
fi
cmp -s /tmp/identity-check /tmp/identity-downloaded || fail 'bootstrap identity check mismatch'
printf 'PASS: private buckets and restricted worker identity configured\n'
