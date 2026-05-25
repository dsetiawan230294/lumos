"""iOS example scenario: drive Safari via xcrun simctl / WebDriverAgent.

This minimal example assumes you control the device via Appium-XCUITest or
the bundled `idb`. It demonstrates how Lumos scenarios are platform-agnostic:
all device interaction is delegated to whatever tool you prefer.

Lumos config:
    app:
      ios: com.apple.mobilesafari
    scenarios:
      - name: ios_safari_scroll
        script: ./scenarios/examples/ios_safari_scroll.py
        iterations: 2
        warmup: 1
"""

from __future__ import annotations

import subprocess
import time

from lumos import Device, log, mark_end, mark_start


def _idb(udid: str, *args: str) -> None:
    subprocess.run(
        ["idb", "--udid", udid, *args],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


def setup(device: Device) -> None:
    log("info", f"setup on {device.id}")
    # Bring Safari to foreground.
    _idb(device.id, "launch", "com.apple.mobilesafari")
    time.sleep(1.5)


def run(device: Device, iteration: int) -> None:
    mark_start("scroll")
    for _ in range(6):
        # Simulate a swipe from y=600 to y=200 at x=200.
        _idb(device.id, "ui", "swipe", "200", "600", "200", "200")
        time.sleep(0.3)
    mark_end("scroll")


def teardown(device: Device) -> None:
    _idb(device.id, "terminate", "com.apple.mobilesafari")
    log("info", "teardown done")
