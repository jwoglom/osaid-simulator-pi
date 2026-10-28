# osaid-simulator-pi

An SD card image that turns a **Raspberry Pi Zero W or Zero 2 W** into a plug-in
appliance for testing automated insulin delivery apps against simulated devices.
Plug the Pi into a laptop over USB and it shows up as a network adapter; open
**http://10.55.0.1/** (or `http://osaid.local/`) to choose which simulator the Pi's
Bluetooth radio is pretending to be:

| Simulator | Pretends to be | Upstream |
|---|---|---|
| g7sim | Dexcom G7 / ONE+ / Stelo sensor | [LoopKit/g7sim](https://github.com/LoopKit/g7sim) |
| pod-dash | Omnipod DASH pod | [LoopKit/pod](https://github.com/LoopKit/pod) |
| pod-five | Omnipod 5 pod | [LoopKit/pod, branch `o5-sim-support`](https://github.com/LoopKit/pod/tree/o5-sim-support) |
| faketandem | Tandem Mobi pump | [jwoglom/faketandem](https://github.com/jwoglom/faketandem) |

These are development tools for exercising app code. None of them is a medical
device, and nothing here is medical advice.

## Use

1. Flash `osaid-simulator-<version>.img.xz` from the Releases page with Raspberry Pi
   Imager. Turn **off** Imager's OS customisation; the image already sets everything.
2. Connect the Pi's **USB data port** (the one nearer the middle, labelled USB, not
   PWR) to the laptop with a data-capable cable. The Pi is powered through it.
3. After about 40 seconds a new network adapter appears. Open http://10.55.0.1/.
4. Pick a simulator and press **Start**. Only one can run at a time: they all need
   the Pi's single Bluetooth radio. Each has its own control panel on its own port,
   linked from the card.
5. SSH works too: `ssh pi@10.55.0.1`, password `raspberry`.

The laptop keeps its own internet connection: the Pi hands out an address on the USB
link but no default route and no DNS.

**Clock.** The Pi has no clock battery and no internet, and g7sim timestamps readings
at wall-clock time. The admin page sets the Pi's clock from your browser each time it
loads, so open the page before starting a simulator.

### Updating a simulator

Download the repository as a zip on the laptop (GitHub → **Code → Download ZIP**, or
`https://github.com/<owner>/<repo>/archive/<branch-or-sha>.zip`) and drop it on the
simulator's **Builds & updates** panel. The Pi unpacks and compiles it with its
bundled Go toolchain, streams the build log, and switches to it on success. Previous
builds stay for one-click rollback. Expect a few minutes on a Zero 2 W and longer on
a Zero W; the build runs with a memory cap so it cannot disturb a running simulator.

A zip containing `osaid-manifest.json` (`{"name":"g7sim","binary":"g7sim","label":"…"}`)
plus the binary is installed without compiling.

### Optional Wi-Fi

Give the Pi Wi-Fi credentials from the admin page (or in `osaid.yaml` on the boot
volume) if it needs the internet itself: NTP, apt, or **Fetch from GitHub** on a
simulator card. The USB link is unaffected.

### Configuration

`/boot/firmware/osaid.yaml` on the SD card's boot volume defines the simulators: their
build rule, exec line, editable flags, control-panel port and exclusive group. The
admin service renders a systemd unit per entry. Edit it from the page's
*Configuration* panel or on a laptop with the SD card mounted, then reboot. Adding a
new simulator is adding a list entry.

## Hardware

One image serves both boards. Everything in it, including the Go toolchain, the
simulators and the JDK faketandem needs, is built for the ARMv6 Zero W and therefore
also runs on the Zero 2 W. faketandem starts a JVM and is noticeably slow on the
Zero W; the Zero 2 W is recommended for it.

## Development

```
make dev        # admin in mock mode on http://127.0.0.1:8080/ (no systemd; builds run locally)
make test
make push       # cross-compile the admin and hot-replace it on a connected Pi
make image      # prebuild + pi-gen in Docker (slow the first time; CI does this on tags)
```

Image builds cache under `image/cache/`: the Lite base rootfs from pi-gen's stages
0 to 2 and the warmed Go build cache. Later builds skip those stages and only run the
OSAID stage and the image export. CI keeps the same tarballs in the Actions cache,
keyed on the pi-gen ref, `image/config` and `image/versions.json`. Delete
`image/cache/` (or bump `CACHE_VERSION` in the workflow) to force a full rebuild.

See [DESIGN.md](DESIGN.md) for the architecture and the reasoning behind it.
Upstream commits and tool versions baked into the image are pinned in
`image/versions.json`.

## Layout

```
admin/      osaid-admin: config → systemd units, start/stop/kill, zip builds, Wi-Fi, clock, web UI
image/      pi-gen config, the custom stage, prebuild.sh (cross-compiles), build.sh (Docker)
.github/    ci.yml (tests), image.yml (image on tags / manual)
```
