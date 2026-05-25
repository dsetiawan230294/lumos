"""First-class Appium integration for Lumos scenarios.

This module turns a 50-line Appium boilerplate scenario into ~5 lines:

    from lumos import Device
    from lumos.appium import session, traced

    def run(device: Device, iteration: int) -> None:
        with session(device) as driver:
            with traced("login"):
                driver.find_element("id", "user").send_keys("demo")
                driver.find_element("id", "pw").send_keys("secret")
                driver.find_element("id", "go").click()

            with traced("scroll_feed"):
                for _ in range(6):
                    driver.swipe(500, 1500, 500, 500, 200)

What you get for free:

* ``session(device)`` builds the right Appium ``Options`` from the
  ``LUMOS_*`` env (Android → ``UiAutomator2Options``,
  iOS → ``XCUITestOptions``), opens the driver, and tears it down on
  exit — even on exceptions.
* ``traced(label)`` wraps a code block with ``mark_start`` / ``mark_end``
  so Lumos can carve the timeline along your logical phases. Markers
  fire even if the block raises.
* ``TracingDriver`` wraps a real Appium driver and auto-marks tap /
  swipe / find_element calls (opt-in via ``session(..., auto_trace=True)``).
* Sensible defaults for ``server_url`` (``LUMOS_APPIUM_URL`` env or
  ``http://localhost:4723``) and arbitrary capability overrides via
  ``caps={...}``.

The import of ``appium`` itself is lazy so this module is safe to import
in environments where Appium isn't installed; the failure surfaces only
at ``session()`` call time with an actionable error.
"""

from __future__ import annotations

import contextlib
import os
import time
from typing import Any, Iterator

from .device import Device
from .rpc import log, mark_end, mark_start


@contextlib.contextmanager
def traced(label: str) -> Iterator[None]:
    """Emit ``mark_start(label)`` on enter, ``mark_end(label)`` on exit.

    Markers fire even when the block raises, so failure timing is still
    captured in the Lumos report.
    """
    mark_start(label)
    try:
        yield
    finally:
        mark_end(label)


def _build_android_options(device: Device, app_id: str, caps: dict[str, Any]) -> Any:
    from appium.options.android import UiAutomator2Options  # type: ignore

    opts = UiAutomator2Options()
    opts.platform_name = "Android"
    opts.device_name = device.id
    opts.udid = device.id
    opts.app_package = app_id
    # Sensible default that works for most apps; override via caps.
    opts.no_reset = False
    for k, v in caps.items():
        opts.set_capability(k, v)
    return opts


def _build_ios_options(device: Device, app_id: str, caps: dict[str, Any]) -> Any:
    from appium.options.ios import XCUITestOptions  # type: ignore

    opts = XCUITestOptions()
    opts.platform_name = "iOS"
    opts.device_name = device.id
    opts.udid = device.id
    opts.bundle_id = app_id
    for k, v in caps.items():
        opts.set_capability(k, v)
    return opts


@contextlib.contextmanager
def session(
    device: Device,
    *,
    server_url: str | None = None,
    caps: dict[str, Any] | None = None,
    auto_trace: bool = False,
    connect_label: str = "appium_connect",
) -> Iterator[Any]:
    """Open an Appium driver for ``device`` and tear it down on exit.

    The driver is bound to the platform and app id from the Lumos host:
    ``Android`` → ``UiAutomator2Options``, ``iOS`` → ``XCUITestOptions``.

    Args:
        device: Lumos device handle (typically ``Device.from_env()`` or
            the one passed to ``run``).
        server_url: Appium server URL. Defaults to
            ``$LUMOS_APPIUM_URL`` or ``http://localhost:4723``.
        caps: Extra capabilities to merge on top of the defaults.
        auto_trace: If True, wraps the driver in :class:`TracingDriver`
            so tap / swipe / find_element calls auto-emit markers.
        connect_label: Marker label used around the initial connect, so
            the Appium handshake doesn't pollute the first scenario phase.

    Raises:
        RuntimeError: if ``appium-python-client`` is not installed.
        ValueError: if ``device.platform`` is unknown.
    """
    try:
        from appium import webdriver  # type: ignore
    except ImportError as e:  # pragma: no cover - depends on user env
        raise RuntimeError(
            "appium-python-client not installed. Run "
            "`pip install Appium-Python-Client` and start an Appium "
            "server (http://localhost:4723)."
        ) from e

    server_url = server_url or os.environ.get(
        "LUMOS_APPIUM_URL", "http://localhost:4723"
    )
    caps = caps or {}

    plat = (device.platform or "").lower()
    if plat == "android":
        options = _build_android_options(device, device.app_id, caps)
    elif plat == "ios":
        options = _build_ios_options(device, device.app_id, caps)
    else:
        raise ValueError(
            f"lumos.appium.session: unknown platform {device.platform!r}; "
            "expected 'android' or 'ios'"
        )

    log("info", f"appium connect: {device.platform}/{device.id} → {server_url}")
    mark_start(connect_label)
    t0 = time.monotonic()
    try:
        driver = webdriver.Remote(server_url, options=options)
    finally:
        mark_end(connect_label)
    log("info", f"appium connected in {time.monotonic() - t0:.2f}s")

    if auto_trace:
        driver = TracingDriver(driver)

    try:
        yield driver
    finally:
        try:
            driver.quit()
        except Exception as e:  # pragma: no cover - best-effort cleanup
            log("warn", f"appium quit failed: {e}")


class TracingDriver:
    """Thin proxy around an Appium driver that auto-marks UI ops.

    Any attribute access falls through to the wrapped driver; a small
    set of well-known methods (``find_element``, ``tap``, ``swipe``,
    ``scroll``, ``click`` …) are wrapped in :func:`traced` automatically.
    Use this when you don't want explicit ``with traced(...)`` blocks
    around every interaction.

    Markers use the form ``ui.<method>`` so they sort together in the
    Lumos report.
    """

    # Methods whose calls produce a marker.
    _TRACED_METHODS = frozenset(
        {
            "find_element",
            "find_elements",
            "tap",
            "swipe",
            "scroll",
            "drag_and_drop",
            "long_press",
            "press_keycode",
            "long_press_keycode",
            "background_app",
            "activate_app",
            "terminate_app",
        }
    )

    def __init__(self, driver: Any) -> None:
        object.__setattr__(self, "_driver", driver)

    def __getattr__(self, name: str) -> Any:
        attr = getattr(self._driver, name)
        if name in self._TRACED_METHODS and callable(attr):
            label = f"ui.{name}"

            def _wrapped(*args: Any, **kwargs: Any) -> Any:
                with traced(label):
                    return attr(*args, **kwargs)

            return _wrapped
        return attr

    # Magic methods can't be resolved via __getattr__; forward the ones
    # commonly used on driver instances.
    def __repr__(self) -> str:
        return f"TracingDriver({self._driver!r})"
