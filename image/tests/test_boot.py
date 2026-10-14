"""Boot regressions exercised without Pi hardware or root privileges."""

import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest


IMAGE = Path(__file__).resolve().parents[1]
INSTALL = IMAGE / "stage-osaid/00-install"


class BootTests(unittest.TestCase):
    def test_overlay_is_added_despite_cm5_host_overlay(self):
        with tempfile.TemporaryDirectory() as tmp:
            config = Path(tmp) / "config.txt"
            config.write_text("[cm5]\ndtoverlay=dwc2,dr_mode=host\n[all]\n")
            # Exercise the actual boot-config edit from the image installer.
            source = (INSTALL / "01-run.sh").read_text()
            edit = source.split('# The base image has a dwc2 host overlay', 1)[1]
            edit = '# The base image has a dwc2 host overlay' + edit.split('\nif ! grep', 1)[0]
            subprocess.run(["sh", "-ec", edit], env={**os.environ, "BOOT": tmp}, check=True)
            self.assertTrue(config.read_text().endswith(
                "[all]\ndtoverlay=dwc2,dr_mode=peripheral\ngpu_mem=16\n"))

    def test_gadget_setup_retry_and_restart(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            udc = root / "udc"
            udc.mkdir()
            gadget = root / "gadget"
            net = root / "net/usb0"
            net.mkdir(parents=True)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            for command in ("modprobe", "ip", "sleep"):
                fake = bin_dir / command
                fake.write_text("#!/bin/sh\nexit 0\n")
                fake.chmod(0o755)
            if not shutil.which("sha1sum"):
                fake = bin_dir / "sha1sum"
                fake.write_text("#!/bin/sh\nexec shasum -a 1\n")
                fake.chmod(0o755)
            script = (INSTALL / "files/usr/local/sbin/osaid-gadget").read_text()
            for original, replacement in {
                "/sys/kernel/config/usb_gadget/osaid": str(gadget),
                "/sys/class/udc": str(udc),
                "/sys/class/net/usb0": str(net),
                "/boot/firmware/osaid-gadget.conf": str(root / "no-overrides"),
                "/proc/cpuinfo": str(root / "cpuinfo"),
            }.items():
                script = script.replace(original, replacement)
            (root / "cpuinfo").write_text("Serial\t: 0000000012345678\n")
            env = {**os.environ, "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"]}

            def run():
                return subprocess.run(["sh", "-ec", script], env=env, capture_output=True, text=True)

            # Missing hardware fails before leaving behind a partial gadget.
            result = run()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("no UDC", result.stderr)
            self.assertFalse(gadget.exists())
            (udc / "controller").touch()
            # Recover the directory left by the old script's failed first boot.
            gadget.mkdir()
            (gadget / "UDC").touch()
            result = run()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((gadget / "UDC").read_text(), "controller\n")
            for function in ("ncm.usb0", "rndis.usb0"):
                for kind in ("dev_addr", "host_addr"):
                    mac = (gadget / "functions" / function / kind).read_text().strip()
                    self.assertRegex(mac, re.compile(r"^(?:[0-9a-f]{2}:){5}[0-9a-f]{2}$"))
            self.assertTrue((gadget / "configs/c.1/ncm.usb0").resolve().is_dir())
            self.assertTrue((gadget / "configs/c.2/rndis.usb0").resolve().is_dir())
            # A service restart must leave the already bound gadget alone.
            before = (gadget / "UDC").stat().st_mtime_ns
            result = run()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((gadget / "UDC").stat().st_mtime_ns, before)


if __name__ == "__main__":
    unittest.main()
