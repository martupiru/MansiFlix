#!/bin/sh
set +x
set -eu
. /opt/minio/common.sh
load_credentials
wait_ready
[ -s /work/synthetic.mp4 ] || fail 'synthetic fixture is missing'
mc_ok pipe admin/mansiflix-originals/demo/synthetic.mp4 </work/synthetic.mp4
printf 'PASS: synthetic MP4 seeded as mansiflix-originals/demo/synthetic.mp4\n'
sha256sum /work/synthetic.mp4
