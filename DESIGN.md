# osaid-simulator-pi — design

A flashable Raspberry Pi OS image that turns a Pi Zero W / Zero 2 W into a USB
"simulator appliance": plug it into a laptop, it shows up as a network interface,
open `http://10.55.0.1/` (or `http://osaid.local/`) and pick which pump/CGM
simulator is running. SSH is `pi` / `raspberry`.

Simulators carried in the image:

| Simulator | Repo | Language | Needs |
|---|---|---|---|
| g7sim | LoopKit/g7sim | Go | raw HCI on `hci0` |
| pod-dash | LoopKit/pod | Go | raw HCI on `hci0` |
| pod-five | jwoglom/pod @ `five` | Go (+ React UI) | raw HCI on `hci0`; web UI fixed at `:8080`; panel embedded only if `frontend/` was built first |
| faketandem | jwoglom/faketandem | Go | raw HCI on `hci0`; Java and the pumpX2 jar only for the optional `-message-codec pumpx2`/`compare` modes |

## Supported hardware: Pi Zero W (gen 1) and Pi Zero 2 W

One image, `armhf`, boots both. Everything in it is held to the **ARMv6 baseline** of
the original Zero W, because anything compiled for ARMv7 dies with `Illegal instruction`
on it, while ARMv6 code runs unchanged on the Zero 2 W.

| | Zero W | Zero 2 W |
|---|---|---|
| CPU | BCM2835, 1× ARM1176 (ARMv6) 1 GHz | BCM2710, 4× Cortex-A53 (ARMv7/arm64) 1 GHz |
| RAM | 512 MB | 512 MB |
| Radio | BT 4.1 + Wi-Fi (BCM43438) | BT 4.2 + Wi-Fi (BCM43436/43438) |
| USB OTG gadget | yes | yes |
| Kernel used from the armhf image | `kernel.img` | `kernel7.img` |

What that forces, component by component:

- **Simulator and admin binaries:** `GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=0`, in CI
  and in the on-device build (the admin sets `GOARM=6` in the build environment even on
  a Zero 2 W, so a binary built there also runs on a Zero W if the card is moved).
- **Go toolchain:** the official `go*.linux-armv6l.tar.gz`.
- **JDK:** the JsBergbau ARMv6hf build (the distro JRE refuses ARMv6). Runs on both.
- **Packages:** only from the Raspberry Pi OS armhf repos, which are built for ARMv6.
  No third-party `.deb`s.
- **Image:** pi-gen `master` branch (32-bit). The Zero 2 W could run the arm64 image but
  gains nothing here, and a single artifact is simpler to ship and test.
- **Boot config:** `dtoverlay=dwc2,dr_mode=peripheral` is valid on both; the firmware
  picks the right kernel and DTB automatically.
- **GPU memory:** `gpu_mem=16` reserves the minimum firmware memory, returning
  48 MB to Linux compared with a 64 MB allocation. The HDMI text console remains
  available; these simulators do not use GPU acceleration or video codecs.

What differs at runtime, handled by the admin at boot (it reads `/proc/device-tree/model`):

- **Build parallelism:** `-p=1` on the Zero W, `-p=2` on the Zero 2 W (memory is the
  limit on both, not cores).
- **Expectations shown in the UI:** build-time estimates and a note that faketandem's
  JVM is slow to start on the Zero W.

Not supported: the Pi Zero without "W" (no radio) and the Zero 2 W's arm64-only tricks.
Larger Pis (3/4/5) would boot the image too, but they lack USB gadget mode on their
main ports (the Pi 4 can do it over USB-C power port; not a goal here).

## Hard constraints discovered up front

1. **One simulator at a time.** All three use the `paypal/gatt` raw-HCI stack and
   take exclusive ownership of `hci0`. The Zero has one radio, and its only data
   USB port is the gadget link to the laptop, so a second adapter is not an option.
   The UI is therefore a single "active simulator" selector, not a set of toggles.
2. **`bluetoothd` must not hold the adapter.** On trixie, `bluetooth.service` grabs
   `hci0` and the sims fail with `no supported devices available`. The image disables
   it; each sim unit declares `Conflicts=bluetooth.service`. `hciuart.service`
   (from `pi-bluetooth`) must stay, as it loads the radio firmware and creates `hci0`.
   Each sim unit also runs `rfkill unblock bluetooth` as root before startup:
   masking bluetoothd does not clear a soft block restored by the base OS. The
   simulator continues to run as `osaid` with its raw-HCI capabilities.
3. **faketandem no longer needs Java.** Since faketandem ported pumpX2's message library and
   EC-JPAKE to Go (`-message-codec native`, `-jpake-mode go`, both the defaults), the unit
   runs without a JVM and the exec line passes no `-java-cmd`/`-pumpx2-jar-path`. The JDK
   and jar stay in the image so `-message-codec compare|pumpx2` can be set via `${FLAGS}`.
   For that optional path: the distro JRE
   does not run on the ARMv6 Zero W (`Server VM is only supported on ARMv7+ VFP`). The
   image instead ships the ARMv6hf OpenJDK 17 build from
   [JsBergbau/OpenJDK-Raspberry-Pi-Zero-W-armv6](https://github.com/JsBergbau/OpenJDK-Raspberry-Pi-Zero-W-armv6)
   (`jdk-17-ga`, built against glibc 2.28, so it runs on trixie) at `/opt/osaid/jdk`.
   It runs on both Zero models under the armhf image, so one JRE covers everything.
   Optional trim: `jlink` the armv6 jmods from an x86 JDK 17 in CI to a ~50 MB runtime.
   The JVM is slow on the Zero W, which no longer matters for the default native mode.
   The pumpX2 cliparser jar (`v1.9.2`, from jitpack) is baked into the image and can be
   replaced from the admin UI if upstream bumps it.
4. **No RTC, no internet.** g7sim timestamps readings at wall-clock time and the phone
   rejects a sensor whose clock is off. The admin page pushes the browser's time to the
   Pi on every load (`POST /api/time`). `fake-hwclock` covers reboots.
5. **No default route through the Pi.** The laptop must keep its own internet. The Pi's
   DHCP server hands out an address only: no router option, no DNS option.

## Base image and build tool

- **Base:** Raspberry Pi OS Lite, trixie, **32-bit armhf**, so a single image boots on
  both the Zero W and Zero 2 W (see the hardware section above for the ARMv6 rules). The image also carries a Go toolchain (the official `armv6l`
  tarball, trimmed of docs/tests) so simulators can be rebuilt on the device from an
  uploaded source zip.
- **Tool:** `pi-gen` (RPi-Distro), run via `build-docker.sh`. Stages 0–2 produce Lite;
  stages 3–5 are skipped (`SKIP` files) and we add `stage-osaid` after stage 2.
  It runs under Docker on a Linux CI runner (GitHub Actions `ubuntu-latest`), and
  works on a Mac with Docker Desktop for local builds, just slowly.
  Lighter alternative if pi-gen feels heavy: `pimod` (Dockerfile-style `Pifile` applied
  to the official Lite `.img`). Same custom-stage contents either way.
- **pi-gen `config`:**
  ```
  IMG_NAME=osaid-simulator
  RELEASE=trixie
  DEPLOY_COMPRESSION=xz
  TARGET_HOSTNAME=osaid
  FIRST_USER_NAME=pi
  FIRST_USER_PASS=raspberry
  DISABLE_FIRST_BOOT_USER_RENAME=1
  ENABLE_SSH=1
  LOCALE_DEFAULT=en_US.UTF-8
  KEYBOARD_KEYMAP=us
  TIMEZONE_DEFAULT=UTC
  STAGE_LIST="stage0 stage1 stage2 stage-osaid"
  ```

## `stage-osaid` contents

```
stage-osaid/
  00-packages                 dnsmasq avahi-daemon pi-bluetooth bluez-firmware rpi-swap rfkill
  01-run.sh                   copies files/, installs Go toolchain + ARMv6 JDK + jar, seeds
                              initial sim checkouts and prebuilt binaries, enables/disables units
  files/
    boot/config.txt.d         dtoverlay=dwc2,dr_mode=peripheral
    boot/cmdline.txt          append: modules-load=dwc2
    etc/modules-load.d/osaid.conf       libcomposite
    usr/local/sbin/osaid-gadget         configfs gadget setup script
    etc/systemd/system/osaid-gadget.service
    etc/NetworkManager/system-connections/usb0.nmconnection
    etc/dnsmasq.d/usb0.conf
    etc/systemd/system/osaid-admin.service
    etc/systemd/journald.conf.d/volatile.conf
    boot/firmware/osaid.yaml            the simulator config (see below)
    opt/osaid/{go,jdk,admin,jars}/
```

`01-run.sh` also: `systemctl disable bluetooth`; `systemctl mask bluetooth`;
disable `apt-daily*.timer`, `unattended-upgrades`, `man-db.timer`; `rfkill block wifi`
by default (opt-in via the YAML or the admin UI, below); use Trixie's native
`rpi-swap`/`systemd-zram-generator` swap management and mask `zramswap.service`
so two managers cannot reconfigure the same zram device; place each simulator's pinned source tree
and a CI-prebuilt binary under `/var/lib/osaid/sims/<name>/` so the image works out of
the box without a first build.

### USB gadget (`osaid-gadget`)

configfs composite gadget with fixed VID/PID, serial and **fixed MAC addresses** (so the
laptop sees the same interface every time). Functions, in this order:

1. `ncm.usb0` — CDC NCM. Native on macOS, Linux and Windows 11.
2. `rndis.usb0` — second configuration with Microsoft OS descriptors, for Windows 10.
   Note Windows 11 24H2 removed the inbox RNDIS driver, which is why NCM comes first.

Rather than `g_ether`, `libcomposite` is used so both functions can be offered.
Verify on a Windows machine; macOS and Linux are the straightforward cases.

### Network on `usb0`

- NetworkManager keyfile: `ipv4.method=manual`, `10.55.0.1/24`, `ipv6.method=disabled`,
  never-default. (`10.55.0.0/24` is chosen to avoid colliding with home/office LANs.)
- dnsmasq bound to `usb0` only:
  ```
  interface=usb0
  bind-interfaces
  dhcp-range=10.55.0.10,10.55.0.50,255.255.255.0,1h
  dhcp-option=option:router          # empty: no default route via the Pi
  dhcp-option=option:dns-server      # empty: laptop keeps its own DNS
  port=0                             # no DNS service at all
  ```
- avahi publishes `osaid.local` on `usb0` as a convenience; `10.55.0.1` is the
  documented address.
- Admin UI on port 80. Sim control panels get distinct ports: g7sim `:8081`, pod-dash
  `:8082`, faketandem `:8083` (their defaults are all `:8080`, overridden by flag), and
  pod-five keeps its hardcoded `:8080`.

### Simulator config on the boot volume: `/boot/firmware/osaid.yaml`

The list of simulators is **data, not baked-in units**. It lives on the FAT boot
partition, so it can be edited from the admin UI *or* by mounting the SD card on a
laptop. The admin service owns it: on boot and on every edit it renders one systemd
unit per entry into `/run/systemd/system/osaid-sim-<name>.service` (ephemeral, so the
YAML is always the source of truth), runs `daemon-reload`, and reconciles running units.

```yaml
version: 1
network:
  address: 10.55.0.1/24
wifi:                        # optional fallback uplink; absent or enabled: false = radio blocked
  enabled: false
  ssid: ""
  psk: ""
  country: US
defaults:
  user: osaid
  capabilities: [CAP_NET_RAW, CAP_NET_ADMIN]
  memory_max: 160M
  restart: always
# exec runs through /bin/sh with PATH prefixed by the active build directory and
#   OSAID_BUILD = active build dir, OSAID_DATA = persistent state dir (the cwd),
#   FLAGS = the editable flags string (lives in flags.env, seeded from here).
simulators:
  - name: g7sim
    title: Dexcom G7 (g7sim)
    source: { github: LoopKit/g7sim, ref: main }   # display, zip matching, "fetch"; never contacted by the Pi on its own
    build:  { kind: go, package: ., output: g7sim }
    exec:   g7sim -api :8081 ${FLAGS}
    flags:  ""
    ui_port: 8081
    exclusive: radio                               # mutual-exclusion group
    # autostart: true                              # start at boot
  - name: pod-dash
    title: Omnipod DASH (pod)
    source: { github: LoopKit/pod, ref: main }
    build:  { kind: go, package: ., output: pod }
    exec:   pod -api :8082 ${FLAGS}
    flags:  -q
    ui_port: 8082
    exclusive: radio
  - name: pod-five
    title: Omnipod 5 (pod, branch five)
    source: { github: jwoglom/pod, ref: five }
    build:  { kind: go, package: ., output: pod }
    exec:   pod -mode o5 ${FLAGS}                  # :8080 is hardcoded in this branch
    flags:  -q
    ui_port: 8080
    exclusive: radio
  - name: faketandem
    title: Tandem Mobi (faketandem)
    source: { github: jwoglom/faketandem, ref: main }
    build:  { kind: go, package: ., output: faketandem }
    exec:   faketandem -api-addr :8083 -ui-dir ${OSAID_BUILD}/ui ${FLAGS}
    flags:  ""
    ui_port: 8083
    memory_max: 320M
    exclusive: radio
```

The authoritative copy of this default is `config.Default` in `admin/internal/config`;
`osaid-admin -print-default-config` prints it and the image build puts it on the boot
volume.

Adding a future simulator is a new list entry (form or raw-YAML editor in the UI);
`build.kind` can be `go`, `prebuilt` (zip already contains the binary), or `script`
(run `./build.sh` in the tree) to cover non-Go tools later. `exclusive` is a generic
group name rather than a hard-coded "only one" rule, so a future non-radio service
can run alongside a radio one.

Rendered unit (g7sim):

```
[Unit]
Description=OSAID simulator: g7sim
Conflicts=bluetooth.service osaid-sim-faketandem.service osaid-sim-pod-dash.service osaid-sim-pod-five.service   # from `exclusive`
After=hciuart.service osaid-admin.service

[Service]
User=osaid
WorkingDirectory=/var/lib/osaid/sims/g7sim/data           # persistent state; survives rebuilds
EnvironmentFile=-/var/lib/osaid/sims/g7sim/flags.env
Environment=OSAID_BUILD=/var/lib/osaid/sims/g7sim/current OSAID_DATA=… OSAID_NAME=g7sim
ExecStart=/var/lib/osaid/sims/g7sim/run.sh                 # "exec g7sim -api :8081 ${FLAGS}" with PATH=$OSAID_BUILD:$PATH
AmbientCapabilities=CAP_NET_RAW CAP_NET_ADMIN
Restart=always
RestartSec=2
KillMode=control-group        # faketandem's java child dies with it
TimeoutStopSec=5              # SIGTERM, then SIGKILL after 5 s
MemoryMax=160M
OOMPolicy=kill
OOMScoreAdjust=500
```

`AmbientCapabilities` replaces `setcap`, so a freshly built binary needs no extra step.
The exec line goes through a generated `run.sh` rather than `ExecStart` quoting, so
any shell-ish exec line from the YAML works unchanged. State files (`g7state.json`,
`state.toml`, faketandem's state) live in `data/` and survive rebuilds; `current` is
an atomic symlink swap to `builds/<id>/src`.

### Process control and OOM protection

- **The admin service is never the OOM victim.** `osaid-admin.service` runs with
  `OOMScoreAdjust=-1000` (kernel will not pick it), `MemoryMin=64M`, `Nice=-10`,
  `CPUWeight=1000`, `Restart=always`, `RestartSec=1`, `WatchdogSec=30` (sd_notify
  heartbeat), and `StartLimitIntervalSec=0`. It is a small static Go binary (~15 MB RSS).
- **Simulators are the preferred victims.** Each gets `MemoryMax` from the YAML,
  `OOMPolicy=kill`, `OOMScoreAdjust=500`. A runaway sim is killed by its own cgroup
  limit long before the system is under pressure.
- **Builds are the most expendable.** Each build runs as a transient unit
  (`systemd-run --unit=osaid-build-<name> -p MemoryMax=350M -p MemorySwapMax=1G
  -p OOMScoreAdjust=1000 -p CPUWeight=50 …`) with `GOFLAGS=-p=1 GOMAXPROCS=1` and
  swap enabled, so a Go compile on the Zero W is slow but bounded and cannot touch the
  admin service or a running simulator.
- **Force kill** is `systemctl kill --signal=SIGKILL osaid-sim-<name>.service`, which
  kills the whole cgroup (binary plus any child like faketandem's JVM). Exposed as a
  separate "Force kill" action next to "Stop".
- **Parameter changes** (flags, env, memory limit, exec) edit the YAML, re-render the
  unit, `daemon-reload`, and restart only that unit.

### Optional Wi-Fi uplink (fallback only)

The Pi is normally air-gapped, but the admin UI (and the YAML, so it can be pre-set
before first boot by editing the SD card) can give it Wi-Fi credentials for the
occasional case where it needs the internet itself: pulling apt packages, Go modules an
upstream forgot to vendor, a newer jar, or simply NTP.

- Stored in the `wifi:` block of `osaid.yaml`. The admin applies it with `nmcli`
  (NetworkManager is already the network stack on Pi OS Lite): a `wlan0` keyfile with
  the SSID/PSK and regulatory country, `rfkill unblock wifi` when `enabled: true`,
  `rfkill block wifi` plus connection down when false. Status (associated, IP, internet
  reachable) is shown in the UI; `systemd-timesyncd` syncs the clock automatically once
  up, which also removes the need for the browser-time push while connected.
- `usb0` stays `never-default`, so adding Wi-Fi gives the Pi a default route via `wlan0`
  without changing anything on the laptop side. The laptop's own internet is untouched
  either way.
- With the uplink up the UI unlocks two conveniences: **"Fetch from GitHub"** on a
  simulator card (admin downloads `https://github.com/<owner>/<repo>/archive/<ref>.zip`
  itself and feeds the same build path as an upload), and an **apt/Go module** escape
  hatch for SSH users. Neither is required for normal operation.
- The YAML lives on a FAT partition, so the PSK is stored in plain text. That matches
  the stated threat model (an appliance that is never on an untrusted network) but is
  worth a note in the README; the UI masks the field.

## Admin service (`osaid-admin`)

A single static Go binary (same toolchain as the sims, cross-compiled in CI) with the
web UI embedded (`embed.FS`, plain HTML/JS, no build step). Runs as root; security is
explicitly out of scope for an air-gapped USB appliance.

API:

| Endpoint | Purpose |
|---|---|
| `GET /api/status` | per-sim current build label, unit state, active `exclusive` holder, `hci0` present, uptime, clock, usb0 link, memory |
| `GET/PUT /api/config` | the YAML from the boot volume (structured + raw); PUT validates, writes, re-renders units, `daemon-reload`, reconciles |
| `POST /api/sims/{name}/start`, `/stop`, `/restart`, `/kill` | `systemctl …`; `/kill` is SIGKILL on the cgroup; start of one stops its `exclusive` group via `Conflicts` |
| `PUT /api/sims/{name}/flags` | writes `flags.env` and restarts if running |
| `GET /api/sims/{name}/logs` | SSE stream of `journalctl -u … -f` |
| `GET /api/sims/{name}/builds` | list of builds (zip name, sha256, built_at), which is `current` |
| `POST /api/sims/{name}/upload` | multipart zip (GitHub "Download ZIP" of the repo, or a prebuilt zip); unpacks, starts a build unit, streams its log; on success swaps `current` |
| `POST /api/sims/{name}/activate/{build}` | roll back / forward by re-pointing `current` and restarting |
| `POST /api/jars` | upload a replacement pumpX2 cliparser jar |
| `POST /api/time` | `{unix, tz}` from the browser; sets system clock (`fake-hwclock save` after) |
| `POST /api/system/reboot`, `/shutdown` | |

The UI page: active-simulator selector, per-sim flags + "open control panel" link
(`http://10.55.0.1:8081/` etc.), live log pane, clock status, a zip drop-zone per simulator with build log, and the config editor.

## Updates: upload the repo's zip, build on the Pi

Upstream repos publish no releases, and GitHub's release/tarball downloads do not allow
cross-origin fetches from a page served by the Pi (measured: Pages and
`raw.githubusercontent.com` send `*`, release assets send nothing, `codeload` allows only
GitHub's renderer). Rather than run a release channel, the user downloads the repo zip
themselves and uploads it:

1. On GitHub, **Code → Download ZIP** (or `…/archive/<sha>.zip`) on the laptop.
2. In the admin UI, drop the zip on the simulator's card. The admin identifies which
   simulator it is from the `module` line in `go.mod` (`github.com/loopkit/g7sim` …) and
   refuses a mismatch unless overridden.
3. The admin unpacks to `/var/lib/osaid/sims/<name>/builds/<zip-sha256-prefix>/`, records
   the zip's top-level directory name (`g7sim-main`, `pod-<sha>`) and hash as the version
   label, and runs the build as a transient unit with `go build` (`-mod=vendor` where a
   `vendor/` exists). The build log streams to the page.
4. On success `current` is re-pointed and the unit restarted; on failure nothing changes.
   Previous builds stay for one-click rollback.

**Offline builds are verified feasible:** g7sim's only dependency is replaced to an
in-tree copy, and pod and faketandem vendor everything, so no module download is needed.
All three declare `go 1.15`, so a current toolchain compiles them. If an upstream ever
adds an unvendored dependency, the build fails loudly with a clear message; the fix is a
vendor commit upstream, or a `prebuilt` zip.

**Prebuilt zips** are also accepted: a zip containing `osaid-manifest.json` plus the
binary (and `ui/` for faketandem) skips the build. This repo's CI can produce such zips
for people who do not want to wait for a Zero W compile.

**Build times (estimates, to measure):** Zero 2 W a minute or two per sim; Zero W
several minutes for pod/g7sim and possibly 10+ for faketandem, with swap active.

**Scope:** the zip path covers each simulator independently. Kernel, packages, Go
toolchain, JDK and the admin binary itself change only by reflashing (an
`osaid-admin` self-update via the same upload mechanism is a cheap later addition).

## CI

- `image.yml` (on tag, or manual): `prebuild.sh` on the runner downloads the Go
  `armv6l` toolchain, the JsBergbau JDK and the pumpX2 jar (sha256-pinned in
  `versions.json`), fetches each simulator's archive zip at its pinned commit and
  cross-compiles it (`GOARCH=arm GOARM=6 CGO_ENABLED=0`); then `build.sh` runs pi-gen in
  Docker. The stage seeds each simulator as a regular build (`builds/seed-<sha8>/src`
  with the source tree, binary and `build.json`), so the image boots working and the
  seed shows up in the UI's build list like any upload. Optionally warms the Go build
  cache inside the chroot. Uploads `osaid-simulator-<ver>.img.xz` to a Release. Flash
  with Raspberry Pi Imager with OS customisation turned off.
- `ci.yml`: gofmt, vet, tests, ARMv6 cross-build, shell syntax, on every push.
- **Caching.** `build.sh` bind-mounts `image/cache` into the pi-gen container as
  `/osaid-cache`. The stage's `03-run.sh` saves the stage-2 (Lite) rootfs there as a
  tarball with numeric ids, xattrs and ACLs, plus the warmed Go build cache. When the
  base tarball exists, `build.sh` marks stages 0-2 `SKIP` and the stage's `prerun.sh`
  restores it as the previous rootfs, so a rebuild is our stage plus the export. CI
  stores the same tarballs with `actions/cache`, keyed on the pi-gen ref and
  `image/config` (base) and `image/versions.json` (Go cache, prebuilt simulators,
  downloads); the base is saved even if a later step fails.

## Repo layout

```
admin/            osaid-admin Go module (config/, units/, builds/, api/, web/ embedded)
image/            pi-gen config, stage-osaid/, versions.json (pinned upstream commits + tool hashes)
.github/workflows/image.yml, prebuilt.yml
DESIGN.md, README.md
```

## Open points to verify on hardware

1. Java on ARMv6 (Zero W): distro JRE vs bundled Zulu/Liberica ARMv6-HF.
2. Gadget on Windows 10 vs 11 (NCM vs RNDIS ordering and MS OS descriptors).
3. That `hciuart.service` brings up `hci0` on trixie with `bluetooth.service` masked
   (g7sim's README implies yes on the Zero W, but with the daemon only stopped).
4. Whether faketandem on a Zero W is usable at all given JVM startup time and 512 MB RAM.

## Later / optional

- Read-only root with an overlay and a writable `/var/lib/osaid` + `/opt/osaid` partition,
  so pulling the USB cable never corrupts the card.
- Expose faketandem's `-transport virtual` TCP link over `usb0` for the iOS Simulator.
