#!/usr/bin/env bash
# Builds the image with pi-gen inside Docker. Run image/prebuild.sh first.
#   image/build.sh            # docker (Linux or macOS with Docker Desktop)
#   NATIVE=1 image/build.sh   # plain ./build.sh on a Debian host (needs sudo)
set -euo pipefail
cd "$(dirname "$0")"
[ -d stage-osaid/00-install/files/prebuilt/admin ] || { echo "run image/prebuild.sh first"; exit 1; }
REF=$(python3 -c "import json; print(json.load(open('versions.json'))['pi_gen']['ref'])")
if [ ! -d pi-gen ]; then
  git clone https://github.com/RPi-Distro/pi-gen.git pi-gen
fi
git -C pi-gen fetch -q origin "$REF" && git -C pi-gen checkout -q "$REF"
# The Docker build context is the pi-gen directory itself (Dockerfile: COPY . /pi-gen/),
# so our stage and its inputs have to be inside it.
rsync -a --delete stage-osaid/ pi-gen/stage-osaid/
cp versions.json pi-gen/versions.json
{ cat config; echo "OSAID_WARM_GOCACHE=${OSAID_WARM_GOCACHE:-1}"; } > pi-gen/config
# Lite's own image would otherwise be exported too.
touch pi-gen/stage2/SKIP_IMAGES
cd pi-gen
if [ "${NATIVE:-0}" = 1 ]; then
  sudo ./build.sh
else
  PRESERVE_CONTAINER=${PRESERVE_CONTAINER:-0} CONTINUE=${CONTINUE:-0} ./build-docker.sh
fi
mkdir -p ../deploy && cp -v deploy/*.img.xz ../deploy/ 2>/dev/null || cp -v deploy/* ../deploy/
echo "image in image/deploy/"
