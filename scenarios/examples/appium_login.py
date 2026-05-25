"""Appium example scenario for Lumos — uses the first-class ``lumos.appium``
adapter, so a real benchmark fits in a dozen lines.

Lumos itself does **not** require Appium; it just runs your Python script as
a subprocess and listens for markers. This example shows the recommended
shape for an Appium-driven scenario.

Prereqs (only if you actually want to run this example):

    pip install Appium-Python-Client
    # …and an Appium server running on http://localhost:4723

Lumos config (config.yaml):

    app:
      android: com.example.app
    scenarios:
      - name: appium_login
        script: ./scenarios/examples/appium_login.py
        iterations: 3
        warmup: 1
        timeout_sec: 60s

Then:

    lumos run config.yaml --trace
"""

from __future__ import annotations

from lumos import Device
from lumos.appium import session, traced


def run(device: Device, iteration: int) -> None:
    # ``session`` builds the right Appium Options from the Lumos env,
    # opens the driver, and quits it on exit (even on exceptions).
    with session(device) as driver:
        with traced("login"):
            driver.find_element("id", "com.example.app:id/username").send_keys("demo")
            driver.find_element("id", "com.example.app:id/password").send_keys("secret")
            driver.find_element("id", "com.example.app:id/login_btn").click()

        with traced("scroll_feed"):
            for _ in range(6):
                driver.swipe(500, 1500, 500, 500, 200)
