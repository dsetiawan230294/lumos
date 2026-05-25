"""Tests for ``lumos.appium`` that don't require a real Appium install.

Run directly::

    python3 pkg/lumos/python/tests/test_appium.py

The tests stub out the ``appium`` and ``appium.options.*`` modules with
``unittest.mock``, then drive the public adapter API. We assert that
``session`` builds the right Options class per platform, that ``traced``
emits the expected JSON-RPC markers, and that ``TracingDriver`` wraps
known UI methods without touching others.
"""

from __future__ import annotations

import io
import json
import os
import sys
import types
import unittest
from contextlib import redirect_stdout
from unittest import mock

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if ROOT not in sys.path:
    sys.path.insert(0, ROOT)


def _install_fake_appium(driver_factory):
    """Install fake ``appium`` modules into ``sys.modules`` so the adapter
    imports succeed. ``driver_factory(server_url, options)`` returns the
    fake driver to hand back from ``webdriver.Remote``.
    """
    appium = types.ModuleType("appium")
    options_pkg = types.ModuleType("appium.options")
    options_android = types.ModuleType("appium.options.android")
    options_ios = types.ModuleType("appium.options.ios")

    class _Opts:
        def __init__(self):
            self.platform_name = ""
            self.device_name = ""
            self.udid = ""
            self.app_package = ""
            self.bundle_id = ""
            self.no_reset = None
            self._extra = {}

        def set_capability(self, k, v):
            self._extra[k] = v

    options_android.UiAutomator2Options = _Opts
    options_ios.XCUITestOptions = _Opts

    webdriver = types.ModuleType("appium.webdriver")
    webdriver.Remote = driver_factory  # type: ignore[attr-defined]
    appium.webdriver = webdriver  # type: ignore[attr-defined]

    sys.modules.update(
        {
            "appium": appium,
            "appium.webdriver": webdriver,
            "appium.options": options_pkg,
            "appium.options.android": options_android,
            "appium.options.ios": options_ios,
        }
    )


def _markers(stdout_text):
    out = []
    for line in stdout_text.splitlines():
        if not line.startswith("{"):
            continue
        try:
            msg = json.loads(line)
        except Exception:
            continue
        if msg.get("method", "").startswith("lumos.mark"):
            out.append((msg["method"], msg["params"].get("label")))
    return out


class TracedTests(unittest.TestCase):
    def test_emits_start_and_end_markers(self):
        from lumos.appium import traced

        buf = io.StringIO()
        with redirect_stdout(buf):
            with traced("login"):
                pass
        self.assertEqual(
            _markers(buf.getvalue()),
            [("lumos.markStart", "login"), ("lumos.markEnd", "login")],
        )

    def test_emits_end_marker_even_on_exception(self):
        from lumos.appium import traced

        buf = io.StringIO()
        with redirect_stdout(buf):
            with self.assertRaises(RuntimeError):
                with traced("boom"):
                    raise RuntimeError("nope")
        ms = _markers(buf.getvalue())
        self.assertEqual(ms[0], ("lumos.markStart", "boom"))
        self.assertEqual(ms[-1], ("lumos.markEnd", "boom"))


class SessionTests(unittest.TestCase):
    def setUp(self):
        # Each test re-imports a fresh adapter so options stubs work.
        for m in list(sys.modules):
            if m == "lumos.appium" or m.startswith("appium"):
                del sys.modules[m]

    def _make_device(self, platform):
        from lumos import Device

        return Device(id="DEV-1", platform=platform, app_id="com.example", iteration=1)

    def test_android_builds_uia2_options_and_quits_on_exit(self):
        captured = {}

        class FakeDriver:
            def __init__(self):
                self.quit_calls = 0

            def quit(self):
                self.quit_calls += 1

        fd = FakeDriver()

        def factory(url, options=None):
            captured["url"] = url
            captured["options"] = options
            return fd

        _install_fake_appium(factory)
        from lumos.appium import session

        device = self._make_device("android")
        buf = io.StringIO()
        with redirect_stdout(buf):
            with session(
                device, server_url="http://x:4723", caps={"newCommandTimeout": 90}
            ) as drv:
                self.assertIs(drv, fd)
        self.assertEqual(captured["url"], "http://x:4723")
        opts = captured["options"]
        self.assertEqual(opts.platform_name, "Android")
        self.assertEqual(opts.udid, "DEV-1")
        self.assertEqual(opts.app_package, "com.example")
        self.assertEqual(opts._extra.get("newCommandTimeout"), 90)
        self.assertEqual(fd.quit_calls, 1)

        # Connect markers should fire.
        ms = _markers(buf.getvalue())
        labels = [label for _, label in ms]
        self.assertIn("appium_connect", labels)

    def test_ios_builds_xcui_options(self):
        captured = {}

        class FakeDriver:
            def quit(self):
                pass

        _install_fake_appium(
            lambda url, options=None: (
                captured.setdefault("opts", options),
                FakeDriver(),
            )[1]
        )
        from lumos.appium import session

        device = self._make_device("ios")
        buf = io.StringIO()
        with redirect_stdout(buf):
            with session(device):
                pass
        opts = captured["opts"]
        self.assertEqual(opts.platform_name, "iOS")
        self.assertEqual(opts.bundle_id, "com.example")

    def test_unknown_platform_raises(self):
        _install_fake_appium(lambda url, options=None: None)
        from lumos.appium import session

        device = self._make_device("symbian")
        with self.assertRaises(ValueError):
            with session(device):
                pass

    def test_quits_on_exception(self):
        class FakeDriver:
            def __init__(self):
                self.quit_calls = 0

            def quit(self):
                self.quit_calls += 1

        fd = FakeDriver()
        _install_fake_appium(lambda url, options=None: fd)
        from lumos.appium import session

        with self.assertRaises(RuntimeError):
            with redirect_stdout(io.StringIO()):
                with session(self._make_device("android")):
                    raise RuntimeError("body failed")
        self.assertEqual(fd.quit_calls, 1)

    def test_missing_appium_raises_runtime_error(self):
        # No fake installed → import should fail inside session().
        for m in list(sys.modules):
            if m.startswith("appium"):
                del sys.modules[m]
        with mock.patch.dict(sys.modules, {"appium": None}):
            from lumos.appium import session

            with self.assertRaises(RuntimeError):
                with session(self._make_device("android")):
                    pass


class TracingDriverTests(unittest.TestCase):
    def test_wraps_known_methods_only(self):
        from lumos.appium import TracingDriver

        class Inner:
            def __init__(self):
                self.tap_calls = 0
                self.title = "App"

            def tap(self, x, y):
                self.tap_calls += 1
                return "tapped"

            def get_window_size(self):
                return (1080, 1920)

        inner = Inner()
        td = TracingDriver(inner)

        buf = io.StringIO()
        with redirect_stdout(buf):
            self.assertEqual(td.tap(10, 20), "tapped")
            self.assertEqual(td.get_window_size(), (1080, 1920))
            self.assertEqual(td.title, "App")
        ms = _markers(buf.getvalue())
        # Exactly one start/end pair, for the wrapped tap.
        self.assertEqual(
            ms, [("lumos.markStart", "ui.tap"), ("lumos.markEnd", "ui.tap")]
        )
        self.assertEqual(inner.tap_calls, 1)


if __name__ == "__main__":
    unittest.main(verbosity=2)
