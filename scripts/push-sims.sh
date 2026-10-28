#!/usr/bin/env bash
# Brings a running Pi in line with this checkout over the USB link: replaces its
# osaid.yaml with this checkout's default config, then downloads the head of each
# simulator's source branch from GitHub and uploads it, one build at a time,
# following each build log. The Pi activates a build when it succeeds.
#
# The whole config is replaced, including Wi-Fi, flags and autostart; the Pi's
# previous osaid.yaml is saved to a private temp file first.
#
#   scripts/push-sims.sh                 every simulator
#   scripts/push-sims.sh g7sim pod-five  config, then just these simulators
#   PI=192.168.1.20 scripts/push-sims.sh
#   FORCE=1 scripts/push-sims.sh         rebuild even if the zip is already active
#
# Needs curl, git, python3 and Go (to print the default config).
set -euo pipefail
cd "$(dirname "$0")/.."
PI=${PI:-10.55.0.1}
API=http://$PI/api

status=$(curl -sf -m 5 "$API/status") || { echo "no admin at http://$PI/ (is the Pi connected?)"; exit 1; }
[ -z "$(echo "$status" | python3 -c 'import json,sys; print(json.load(sys.stdin)["build_running"])')" ] ||
  { echo "a build is already running on the Pi; wait for it to finish"; exit 1; }

work=$(mktemp -d); trap 'rm -rf "$work"' EXIT

# --- config --------------------------------------------------------------------
(cd admin && go run . -print-default-config) > "$work/osaid.yaml"
curl -sf "$API/config" > "$work/pi.yaml"
if cmp -s "$work/osaid.yaml" "$work/pi.yaml"; then
  echo "have config"
else
  tmp=${TMPDIR:-/tmp}; backup=$(mktemp "${tmp%/}/osaid-$PI-XXXXXX") && chmod 600 "$backup"   # may hold the Wi-Fi password
  cp "$work/pi.yaml" "$backup"
  echo "replace config (previous one saved to $backup)"
  curl -sf -X PUT --data-binary "@$work/osaid.yaml" -H 'Content-Type: text/yaml' "$API/config" >/dev/null ||
    { echo "the Pi refused the config"; exit 1; }
  status=$(curl -sf -m 5 "$API/status")
fi

# --- simulators ----------------------------------------------------------------
# name repo ref, one per line
sources=$(python3 -c '
import re,sys
name=None
for line in open(sys.argv[1]):
    m=re.match(r"\s*- name:\s*(\S+)", line)
    if m: name=m.group(1)
    m=re.match(r"\s*source:\s*\{\s*github:\s*([^,\s]+),\s*ref:\s*([^}\s]+)\s*\}", line)
    if m and name: print(name, m.group(1), m.group(2)); name=None
' "$work/osaid.yaml")

# sha256 of the zip a simulator's active build came from
active_sha() { echo "$status" | python3 -c '
import json,sys
for s in json.load(sys.stdin)["simulators"]:
    if s["name"]==sys.argv[1]: print((s.get("current_build") or {}).get("sha256",""))
' "$1"; }

failed=()
while read -r -u 3 name repo ref; do
  if [ $# -gt 0 ] && [[ " $* " != *" $name "* ]]; then continue; fi
  commit=$(git ls-remote "https://github.com/$repo" "refs/heads/$ref" | cut -f1)
  [ -n "$commit" ] || { echo "$name: no branch $ref in $repo"; failed+=("$name"); continue; }
  short=${commit:0:12}
  zip=$work/$name-$short.zip
  echo "fetch $repo@$ref ($short)"
  curl -sfL --retry 3 -o "$zip" "https://github.com/$repo/archive/$commit.zip"
  sha=$(shasum -a 256 "$zip" | cut -d' ' -f1)
  if [ "$sha" = "$(active_sha "$name")" ] && [ -z "${FORCE:-}" ]; then
    echo "have $name@$short (already the active build)"; continue
  fi

  echo "upload $name@$short"
  resp=$(curl -s -w '\n%{http_code}' -F "zip=@$zip" "$API/sims/$name/upload${FORCE:+?force=1}")
  code=${resp##*$'\n'}; body=${resp%$'\n'*}
  if [ "$code" != 202 ]; then echo "$name: upload refused ($code): $body"; failed+=("$name"); continue; fi
  id=$(echo "$body" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')

  # build log is server-sent events: "data: <line>", then "event: done" + "data: ok|failed"
  result=$(curl -sN "$API/sims/$name/builds/$id/log" | python3 -u -c '
import sys
done=False
for line in sys.stdin:
    line=line.rstrip("\n")
    if line=="event: done": done=True; continue
    if line.startswith("data: "):
        if done: print(line[6:]); break
        print("  "+line[6:], file=sys.stderr)
')
  [ "$result" = ok ] || { echo "$name: build failed"; failed+=("$name"); }
done 3<<<"$sources"

if [ ${#failed[@]} -gt 0 ]; then echo "failed: ${failed[*]}"; exit 1; fi
echo "done"
