#!/usr/bin/env bash
# Prepares everything the pi-gen stage copies into the image:
#   files/downloads/   Go armv6l toolchain, ARMv6 JDK, pumpX2 jar (sha256-verified)
#   files/prebuilt/    cross-compiled simulators (GOARM=6) + their pinned source
#                      zips, the admin binary, and the default osaid.yaml
# Runs on any host with Go >= 1.24, curl, unzip, python3. No Docker needed.
set -euo pipefail
cd "$(dirname "$0")"
V=versions.json
FILES=stage-osaid/00-install/files
DL=$FILES/downloads
PB=$FILES/prebuilt
mkdir -p "$DL" "$PB"

j() { python3 -c "import json,sys; v=json.load(open('$V'))
for k in sys.argv[1].split('.'): v=v[k]
print(v)" "$1"; }

fetch() { # url sha256 dest
  local url=$1 sha=$2 dest=$3
  if [ -f "$dest" ] && echo "$sha  $dest" | shasum -a 256 -c --status 2>/dev/null; then
    echo "have $(basename "$dest")"; return
  fi
  echo "fetch $url"
  curl -fL --retry 3 -o "$dest.part" "$url"
  echo "$sha  $dest.part" | shasum -a 256 -c --status || { echo "sha256 mismatch for $url"; rm -f "$dest.part"; exit 1; }
  mv "$dest.part" "$dest"
}

fetch "$(j go.url)" "$(j go.sha256)" "$DL/go.tar.gz"
fetch "$(j jdk.url)" "$(j jdk.sha256)" "$DL/jdk.tar.gz"
fetch "$(j pumpx2_jar.url)" "$(j pumpx2_jar.sha256)" "$DL/pumpx2-cliparser.jar"

# --- admin ---------------------------------------------------------------------
VERSION=${OSAID_VERSION:-$(git -C .. describe --tags --always --dirty 2>/dev/null || echo dev)}
echo "admin $VERSION"
mkdir -p "$PB/admin"
( cd ../admin && GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "../image/$PB/admin/osaid-admin" . )
( cd ../admin && go run . -print-default-config > "../image/$PB/admin/osaid.yaml" )

# --- simulators ----------------------------------------------------------------
for name in $(python3 -c "import json; print(' '.join(json.load(open('$V'))['simulators']))"); do
  repo=$(j "simulators.$name.repo"); commit=$(j "simulators.$name.commit"); output=$(j "simulators.$name.output")
  short=${commit:0:12}
  dir=$PB/sims/$name
  if [ -f "$dir/build.json" ] && grep -q "\"$commit\"" "$dir/build.json"; then
    echo "have $name@$short"
  else
  rm -rf "${dir:?}"; mkdir -p "$dir"
  zip=$DL/$name-$short.zip
  [ -f "$zip" ] || { echo "fetch $repo@$short"; curl -sfL --retry 3 -o "$zip" "https://github.com/$repo/archive/$commit.zip"; }
  work=$(mktemp -d); unzip -q "$zip" -d "$work"
  src=$(find "$work" -mindepth 1 -maxdepth 1 -type d | head -1)
  # Some simulators embed a web UI that must be built first (pod "five": Vite/React).
  fe=$(python3 -c "import json; print(json.load(open('$V'))['simulators']['$name'].get('frontend',''))")
  if [ -n "$fe" ] && [ -f "$src/$fe/package.json" ]; then
    if command -v npm >/dev/null; then
      echo "build $name frontend ($fe)"
      ( cd "$src/$fe" && npm ci --silent --no-audit --no-fund 2>/dev/null || npm install --silent --no-audit --no-fund; npm run build --silent )
      rm -rf "${src:?}/$fe/node_modules"
    else
      echo "warning: npm not found; $name is built without its embedded web UI"
    fi
  fi
  modflag=""; [ -d "$src/vendor" ] && modflag=-mod=vendor
  echo "build $name@$short"
  ( cd "$src" && GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath $modflag -o "$OLDPWD/$dir/$output" . )
  mv "$src" "$dir/src"      # the whole tree is seeded into the image (ui/, vendor/, …)
  cp "$zip" "$dir/source.zip"
  rm -rf "${work:?}"
  python3 - "$dir/build.json" "$name" "$repo" "$commit" "$output" "$(basename "$zip" .zip)" <<'PY'
import json,sys,datetime,hashlib
p,name,repo,commit,output,label=sys.argv[1:]
sha=hashlib.sha256(open(p.replace('build.json','source.zip'),'rb').read()).hexdigest()
json.dump({"id":"seed-"+commit[:8],"label":f"{repo.split('/')[1]}-{commit[:7]} (image seed)","kind":"go",
  "zip_name":label+".zip","sha256":sha,"started":datetime.datetime.now(datetime.timezone.utc).isoformat(),
  "finished":datetime.datetime.now(datetime.timezone.utc).isoformat(),"running":False,"ok":True,"binary":output,
  "commit":commit,"repo":repo},open(p,'w'),indent=2)
PY
  fi
  printf 'ID=%s\nBINARY=%s\nSHA256=%s\n' "seed-${commit:0:8}" "$output" "$(shasum -a 256 "$dir/source.zip" | cut -d' ' -f1)" > "$dir/build.env"
done
printf 'JDK_TOPDIR=%s\n' "$(j jdk.topdir)" > "$PB/env.sh"
echo "prebuild done:"; du -sh "$DL" "$PB"
