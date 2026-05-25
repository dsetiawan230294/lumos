"""Example Lumos scenario: scroll the app's home screen.

Replace the body of `run` with real automation (Appium, uiautomator2, etc.).
"""

from __future__ import annotations

import time

from lumos import Device, log, mark_end, mark_start


def setup(device: Device) -> None:
    log("info", f"setup on {device.id} ({device.platform})")


def run(device: Device, iteration: int) -> None:
    mark_start("home_scroll")
    # TODO: drive the app — placeholder sleep represents the scroll.
    time.sleep(2.0)
    mark_end("home_scroll")


def teardown(device: Device) -> None:
    log("info", "teardown")
