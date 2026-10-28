#!/bin/sh
# Administrative helpers ONLY, not a worker entrypoint. Never enable xtrace.
set +x
set -eu
umask 077

fail() {
    printf 'FAIL: %s\n' "$1" >&2
    exit 1
}

export MC_CONFIG_DIR=/tmp/mc
export MC_NO_COLOR=1
MINIO_ENDPOINT=http://minio:9000

load_credentials() {
    # Names only: Kubernetes injects the VALUES from secretKeyRef at startup.
    # Missing variables fail without printing their contents.
    : "${MINIO_ROOT_USER:?MINIO_ROOT_USER must be injected from its Secret}"
    : "${MINIO_ROOT_PASSWORD:?MINIO_ROOT_PASSWORD must be injected from its Secret}"
    : "${WORKER_ACCESS_KEY:?WORKER_ACCESS_KEY must be injected from its Secret}"
    : "${WORKER_SECRET_KEY:?WORKER_SECRET_KEY must be injected from its Secret}"
    admin_user=$MINIO_ROOT_USER
    admin_password=$MINIO_ROOT_PASSWORD
    worker_user=$WORKER_ACCESS_KEY
    worker_password=$WORKER_SECRET_KEY
    [ "$admin_user" = mansiflix-admin ] || fail 'unexpected administrator identity'
    [ "$worker_user" = mansiflix-worker ] || fail 'unexpected worker identity'
    [ "${#admin_password}" -ge 32 ] || fail 'administrator password is too short'
    [ "${#worker_password}" -ge 32 ] || fail 'worker password is too short'
    # create-secrets.py generates URL-safe passwords. Refuse incompatible
    # imported values rather than interpolate unescaped credentials into a URL.
    case "$admin_password" in
        ''|*[!A-Za-z0-9_-]*) fail 'administrator password must be URL-safe' ;;
    esac
    case "$worker_password" in
        ''|*[!A-Za-z0-9_-]*) fail 'worker password must be URL-safe' ;;
    esac
    # Credentials are carried in child-process environment, never argv or logs.
    export MC_HOST_admin="http://${admin_user}:${admin_password}@minio:9000"
    export MC_HOST_worker="http://${worker_user}:${worker_password}@minio:9000"
}

wait_ready() {
    attempts=0
    until wget -q -T 2 -t 1 -O /dev/null "$MINIO_ENDPOINT/minio/health/ready" 2>/dev/null; do
        attempts=$((attempts + 1))
        [ "$attempts" -lt 30 ] || fail 'MinIO readiness or network check timed out'
        sleep 2
    done
}

mc_ok() {
    # Do not print mc errors: admin responses may contain credential material.
    if [ "$1" = pipe ]; then
        shift
        set -- pipe --part-size 5MiB --concurrent 1 "$@"
    fi
    if ! mc --json "$@" >/tmp/mc-response.json 2>&1; then
        fail 'MinIO operation failed; inspect connectivity/configuration without dumping credentials'
    fi
}

expect_denied() {
    check_name=$1
    shift
    if [ "$1" = pipe ]; then
        if mc --json pipe --part-size 5MiB --concurrent 1 "$2" >/tmp/mc-denied.json 2>&1; then
            fail "permission unexpectedly granted: $check_name"
        fi
    elif mc --json "$@" >/tmp/mc-denied.json 2>&1; then
        fail "permission unexpectedly granted: $check_name"
    fi
    # A network, DNS, authentication or syntax error must NOT pass a denial test.
    if grep -Eq '"Code"[[:space:]]*:[[:space:]]*"(AccessDenied|XMinioAdminAccessDenied)"' /tmp/mc-denied.json; then
        printf 'PASS denied: %s (S3/admin code)\n' "$check_name"
        return
    fi
    # Pinned mc converts S3 AccessDenied from PutObject/StatObject to
    # PathInsufficientPermission, losing the original Code. Accept ONLY that
    # exact message and matching HTTP S3 path for these reviewed operations.
    # Never accept a generic permission string or an arbitrary command failure.
    denied_target=
    case "$1:$2" in
        pipe:worker/*|cat:worker/*) denied_target=$2 ;;
        rm:--force)
            case "${3:-}" in worker/*) denied_target=$3 ;; esac
            ;;
    esac
    if [ -n "$denied_target" ]; then
            denied_path="$MINIO_ENDPOINT/${denied_target#worker/}"
            # Runtime mc emits compact JSON; tolerate whitespace around JSON
            # separators without weakening the exact message/path comparison.
            sed 's/"[[:space:]]*:[[:space:]]*/":/g' /tmp/mc-denied.json >/tmp/mc-denied-normalized.json
            denied_message="\"message\":\"Insufficient permissions to access this path \`$denied_path\`\""
            denied_field="\"Path\":\"$denied_path\""
            if grep -Fq "$denied_message" /tmp/mc-denied-normalized.json &&
                grep -Fq "$denied_field" /tmp/mc-denied-normalized.json; then
                printf 'PASS denied: %s (pinned mc S3 wrapper)\n' "$check_name"
                return
            fi
            # mc's bulk-removal path loses ErrorResponse.Code, retaining only
            # the exact S3 error text. This validates the CLI rejection only;
            # it does not claim a raw DELETE request reached the server.
            if [ "$1" = rm ] &&
                grep -Fq '"message":"Access Denied."' /tmp/mc-denied-normalized.json &&
                { grep -Fq "$denied_path" /tmp/mc-denied-normalized.json ||
                  grep -Fq "$denied_target" /tmp/mc-denied-normalized.json; }; then
                printf 'PASS denied CLI only: %s (mc Access Denied; raw DELETE unverified)\n' "$check_name"
                return
            fi
    fi
    fail "expected a reviewed authorization-denial response: $check_name"
}
