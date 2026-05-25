"""Integration scenario: open Android Settings and scroll it via adb input swipe.

Designed for the Phase 1 real-device integration test. Uses only adb (no
Appium) so it works on any plugged-in device. The host (Go) starts the
sampler against `com.android.settings` *after* this script launches it.
"""

from __future__ import annotations

import subprocess
import time

from lumos import Device, log, mark_end, mark_start


def _adb(serial: str, *args: str) -> None:
    cmd = ["adb"]
    if serial:
        cmd += ["-s", serial]
    cmd += list(args)
    subprocess.run(
        cmd, check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
    )


def setup(device: Device) -> None:
    log("info", f"setup on {device.id}")
    _adb(device.id, "shell", "am", "force-stop", "com.android.settings")


def run(device: Device, iteration: int) -> None:
    mark_start("launch")
    _adb(device.id, "shell", "am", "start", "-W", "com.android.settings/.Settings")
    time.sleep(1.5)
    mark_end("launch")

    mark_start("scroll")
    # 6 vertical swipes, ~200ms each.
    for _ in range(6):
        _adb(device.id, "shell", "input", "swipe", "500", "1500", "500", "500", "200")
        time.sleep(0.4)
    mark_end("scroll")


def teardown(device: Device) -> None:
    _adb(device.id, "shell", "am", "force-stop", "com.android.settings")
    log("info", "teardown done")
