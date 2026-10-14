#!/bin/bash -e
# Save what the next build can reuse (see image/build.sh): the Lite base rootfs
# (only if not cached yet) and the Go build cache warmed in 02-run-chroot.sh.
: "${ROOTFS_DIR:?}"
[ -n "${OSAID_CACHE:-}" ] && [ -d "${OSAID_CACHE}" ] || { echo "no OSAID_CACHE; nothing saved"; exit 0; }
if [ ! -f "${OSAID_CACHE}/base-rootfs.tar.gz" ] && [ -d "${PREV_ROOTFS_DIR:-/nonexistent}" ]; then
	echo "saving base rootfs to ${OSAID_CACHE}/base-rootfs.tar.gz"
	tar --numeric-owner --xattrs --xattrs-include='*' --acls --exclude=var/cache/apt/archives \
		-cf "${OSAID_CACHE}/base-rootfs.tar.gz.part" -C "${PREV_ROOTFS_DIR}" --use-compress-program=pigz .
	mv "${OSAID_CACHE}/base-rootfs.tar.gz.part" "${OSAID_CACHE}/base-rootfs.tar.gz"
fi
if [ -d "${ROOTFS_DIR}/var/lib/osaid/gocache" ]; then
	echo "saving Go build cache to ${OSAID_CACHE}/gocache.tar.gz"
	tar -cf "${OSAID_CACHE}/gocache.tar.gz.part" -C "${ROOTFS_DIR}/var/lib/osaid" --use-compress-program=pigz gocache
	mv "${OSAID_CACHE}/gocache.tar.gz.part" "${OSAID_CACHE}/gocache.tar.gz"
fi
ls -la "${OSAID_CACHE}"
