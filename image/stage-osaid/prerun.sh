#!/bin/bash -e
# With stages 0-2 skipped, the previous rootfs comes from the cache tarball that a
# previous build's 03-run.sh saved (see image/build.sh).
if [ ! -d "${PREV_ROOTFS_DIR}" ] && [ -n "${OSAID_CACHE:-}" ] && [ -f "${OSAID_CACHE}/base-rootfs.tar.gz" ]; then
	echo "restoring base rootfs from ${OSAID_CACHE}/base-rootfs.tar.gz"
	mkdir -p "${PREV_ROOTFS_DIR}"
	tar --numeric-owner --xattrs --xattrs-include='*' --acls -xf "${OSAID_CACHE}/base-rootfs.tar.gz" -C "${PREV_ROOTFS_DIR}" --use-compress-program=pigz
fi
if [ ! -d "${ROOTFS_DIR}" ]; then
	copy_previous
fi
