#!/bin/bash -e
# Runs on the build host with ${ROOTFS_DIR} mounted. Copies our files in.
: "${ROOTFS_DIR:?}" "${STAGE_DIR:?}"
V="${STAGE_DIR}/../versions.json"
F="${STAGE_DIR}/00-install/files"
[ -f "$F/prebuilt/env.sh" ] || { echo "image/prebuild.sh has not been run"; exit 1; }
. "$F/prebuilt/env.sh"

# static files
install -m 755 "$F/usr/local/sbin/osaid-gadget"            "${ROOTFS_DIR}/usr/local/sbin/osaid-gadget"
install -m 644 "$F/etc/systemd/system/osaid-gadget.service" "${ROOTFS_DIR}/etc/systemd/system/"
install -m 644 "$F/etc/systemd/system/osaid-admin.service"  "${ROOTFS_DIR}/etc/systemd/system/"
install -m 644 "$F/etc/dnsmasq.d/osaid-usb0.conf"           "${ROOTFS_DIR}/etc/dnsmasq.d/"
install -m 644 "$F/etc/NetworkManager/conf.d/osaid.conf"    "${ROOTFS_DIR}/etc/NetworkManager/conf.d/"
install -m 644 "$F/etc/modules-load.d/osaid.conf"           "${ROOTFS_DIR}/etc/modules-load.d/"
install -m 644 "$F/etc/default/zramswap"                    "${ROOTFS_DIR}/etc/default/zramswap"
install -d "${ROOTFS_DIR}/etc/systemd/journald.conf.d"
install -m 644 "$F/etc/systemd/journald.conf.d/osaid.conf"  "${ROOTFS_DIR}/etc/systemd/journald.conf.d/"
install -m 644 "$F/boot/osaid-gadget.conf"                  "${ROOTFS_DIR}/boot/firmware/osaid-gadget.conf"
install -m 644 "$F/prebuilt/admin/osaid.yaml"               "${ROOTFS_DIR}/boot/firmware/osaid.yaml"

# boot config: USB device mode + memory cgroup (MemoryMax= needs it on Pi OS)
BOOT="${ROOTFS_DIR}/boot/firmware"
grep -q '^dtoverlay=dwc2' "$BOOT/config.txt" || printf '\n# OSAID: USB device (gadget) mode on the data port\ndtoverlay=dwc2,dr_mode=peripheral\n' >> "$BOOT/config.txt"
if ! grep -q 'cgroup_enable=memory' "$BOOT/cmdline.txt"; then
	sed '1 s/$/ cgroup_enable=memory cgroup_memory=1 modules-load=dwc2/' "$BOOT/cmdline.txt" > "$BOOT/cmdline.txt.new"
	mv "$BOOT/cmdline.txt.new" "$BOOT/cmdline.txt"
fi

# /opt/osaid: toolchain, JDK, jar, admin
OPT="${ROOTFS_DIR}/opt/osaid"
install -d "$OPT/admin" "$OPT/jars"
install -m 755 "$F/prebuilt/admin/osaid-admin" "$OPT/admin/osaid-admin"
install -m 644 "$F/downloads/pumpx2-cliparser.jar" "$OPT/jars/pumpx2-cliparser.jar"
rm -rf "${OPT:?}/go" "${OPT:?}/jdk"
tar -C "$OPT" -xzf "$F/downloads/go.tar.gz"
rm -rf "${OPT:?}/go/test" "${OPT:?}/go/doc" "${OPT:?}/go/api" "${OPT:?}/go/misc"
tar -C "$OPT" -xzf "$F/downloads/jdk.tar.gz"
mv "$OPT/${JDK_TOPDIR:?}" "$OPT/jdk"
rm -rf "${OPT:?}/jdk/jmods" "${OPT:?}/jdk/lib/src.zip" "${OPT:?}/jdk/demo" "${OPT:?}/jdk/man"
cp "$V" "$OPT/versions.json"
echo "${OSAID_WARM_GOCACHE:-1}" > "$OPT/.warm-gocache"

# seed simulator builds so the image works before any upload
STATE="${ROOTFS_DIR}/var/lib/osaid"
for dir in "$F"/prebuilt/sims/*/; do
	name=$(basename "$dir")
	. "$dir/build.env"   # ID, BINARY, SHA256
	id=$ID; binary=$BINARY; sha=$SHA256
	dest="$STATE/sims/$name/builds/$id"
	install -d "$dest/src" "$STATE/sims/$name/data" "$STATE/sims/$name/uploads"
	cp -a "$dir/src/." "$dest/src/"
	install -m 755 "$dir/$binary" "$dest/src/$binary"
	install -m 644 "$dir/build.json" "$dest/build.json"
	cp "$dir/source.zip" "$STATE/sims/$name/uploads/$sha.zip"
	ln -sfn "/var/lib/osaid/sims/$name/builds/$id/src" "$STATE/sims/$name/current"
done
