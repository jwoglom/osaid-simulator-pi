#!/bin/bash -e
# Runs inside the image (qemu). Users, services, swap, cache warming.
id -u osaid >/dev/null 2>&1 || useradd --system --home-dir /var/lib/osaid --shell /usr/sbin/nologin osaid
chown -R osaid:osaid /var/lib/osaid/sims/*/data

# The simulators drive hci0 over raw HCI; bluetoothd must not hold the adapter.
systemctl disable bluetooth.service || true
systemctl mask bluetooth.service
systemctl enable hciuart.service || true

# Trixie's rpi-swap/systemd-zram-generator owns zram. A second manager fails
# with EBUSY when it tries to change the already active /dev/zram0.
systemctl mask zramswap.service
systemctl enable osaid-gadget.service osaid-admin.service dnsmasq.service avahi-daemon.service
# no internet: these only burn CPU and SD writes
systemctl disable apt-daily.timer apt-daily-upgrade.timer man-db.timer e2scrub_all.timer 2>/dev/null || true
systemctl disable ModemManager.service 2>/dev/null || true

# mDNS only on the USB link and Wi-Fi
sed -i 's/^#\?allow-interfaces=.*/allow-interfaces=usb0,wlan0/' /etc/avahi/avahi-daemon.conf
sed -i 's/^#\?use-ipv6=.*/use-ipv6=no/' /etc/avahi/avahi-daemon.conf

# Keep the native rpi-swap sizing and backing-store policy.

# Warm the Go build cache so the first on-device build does not recompile the
# standard library on a 1 GHz ARMv6 core. Best effort: slow under qemu.
if [ "$(cat /opt/osaid/.warm-gocache 2>/dev/null || echo 1)" = 1 ]; then
	export GOCACHE=/var/lib/osaid/gocache GOPATH=/var/lib/osaid/gopath HOME=/var/lib/osaid
	export GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-trimpath
	/opt/osaid/go/bin/go build std || echo "warning: go build std failed under qemu; cache not warmed"
	for d in /var/lib/osaid/sims/*/builds/*/src; do
		( cd "$d" && f=""; [ -d vendor ] && f=-mod=vendor; /opt/osaid/go/bin/go build $f -o /dev/null . ) || echo "warning: warm $d failed"
	done
fi
chmod -R a+rX /opt/osaid /var/lib/osaid/sims
