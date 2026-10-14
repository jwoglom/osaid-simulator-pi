#!/usr/bin/env bash
# Builds the image with pi-gen inside Docker. Run image/prebuild.sh first.
#   image/build.sh            # docker (Linux or macOS with Docker Desktop)
#   NATIVE=1 image/build.sh   # plain ./build.sh on a Debian host (needs sudo)
#
# Caching (image/cache, or OSAID_CACHE_DIR): after a build, the Lite base rootfs
# (pi-gen stages 0-2, the slow part) and the warmed Go build cache are saved there.
# On the next run stages 0-2 are skipped and the base is restored from the tarball,
# so only our stage and the image export run. Delete image/cache to start over.
set -euo pipefail
cd "$(dirname "$0")"
[ -f stage-osaid/00-install/files/prebuilt/env.sh ] || { echo "run image/prebuild.sh first"; exit 1; }
REF=$(python3 -c "import json; print(json.load(open('versions.json'))['pi_gen']['ref'])")
CACHE_DIR=$(cd "$(mkdir -p "${OSAID_CACHE_DIR:-cache}" && echo "${OSAID_CACHE_DIR:-cache}")" && pwd)
if [ ! -d pi-gen ]; then
  git clone https://github.com/RPi-Distro/pi-gen.git pi-gen
fi
git -C pi-gen fetch -q origin "$REF" && git -C pi-gen checkout -q "$REF"
# The Docker build context is the pi-gen directory itself (Dockerfile: COPY . /pi-gen/),
# so our stage and its inputs have to be inside it.
rsync -a --delete stage-osaid/ pi-gen/stage-osaid/
cp versions.json pi-gen/versions.json
{
  cat config
  echo "OSAID_WARM_GOCACHE=${OSAID_WARM_GOCACHE:-1}"
  echo "CLEAN=1"   # always rebuild OUR stage's rootfs from the base; skipped stages are untouched
} > pi-gen/config
# Lite's own image would otherwise be exported too.
touch pi-gen/stage2/SKIP_IMAGES
# Base rootfs cached? Then skip stages 0-2; stage-osaid/prerun.sh restores the tarball.
rm -f pi-gen/stage0/SKIP pi-gen/stage1/SKIP pi-gen/stage2/SKIP
if [ -f "$CACHE_DIR/base-rootfs.tar.gz" ]; then
  echo "using cached base rootfs from $CACHE_DIR"
  touch pi-gen/stage0/SKIP pi-gen/stage1/SKIP pi-gen/stage2/SKIP
else
  echo "no cached base rootfs; building stages 0-2 (slow, once)"
fi
cd pi-gen
if [ "${NATIVE:-0}" = 1 ]; then
  OSAID_CACHE="$CACHE_DIR" sudo --preserve-env=OSAID_CACHE ./build.sh
else
  # A fresh container every time; the cache directory carries state between runs.
  docker rm -v pigen_work >/dev/null 2>&1 || true
  PIGEN_DOCKER_OPTS="-v $CACHE_DIR:/osaid-cache -e OSAID_CACHE=/osaid-cache ${PIGEN_DOCKER_OPTS:-}" ./build-docker.sh
fi
mkdir -p ../deploy && cp -v deploy/*.img.xz ../deploy/ 2>/dev/null || cp -v deploy/* ../deploy/
echo "image in image/deploy/"
ls -la "$CACHE_DIR"
