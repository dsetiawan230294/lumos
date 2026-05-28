#!/usr/bin/env python3
"""Lumos scenario harness.

Loaded by the Lumos Go binary via:

    python3 harness.py /path/to/scenario.py

Imports the user's scenario module dynamically, calls its ``setup`` / ``run``
/ ``teardown`` functions, and forwards markers/logs/attachments to the host
as JSON-RPC notifications on stdout (provided by the vendored ``lumos``
Python helper package).

The harness is intentionally tiny and dependency-free so it can be shipped
inside the CLI binary and dropped onto disk at runtime.
"""

from __future__ import annotations

import importlib.util
import json
import os
import sys
import traceback
from types import ModuleType


def _emit(method: str, **params: object) -> None:
    sys.stdout.write(
        json.dumps({"jsonrpc": "2.0", "method": method, "params": params}) + "\n"
    )
    sys.stdout.flush()


def _load(path: str) -> ModuleType:
    # Mirror the behavior of `python script.py`: make the script's directory
    # the first entry on sys.path so the script can import sibling modules
    # (helpers, page-object files, project-local config, etc.) without any
    # extra setup. Idempotent — repeated invocations don't duplicate entries.
    script_dir = os.path.abspath(os.path.dirname(path))
    if script_dir and script_dir not in sys.path:
        sys.path.insert(0, script_dir)

    spec = importlib.util.spec_from_file_location("lumos_scenario", path)
    if spec is None or spec.loader is None:
        raise ImportError(f"cannot load scenario module: {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)  # type: ignore[attr-defined]
    return mod


def main(argv: list[str]) -> int:
    if len(argv) < 2:
        _emit("lumos.log", level="error", msg="harness: missing scenario path")
        return 2

    iteration = int(os.environ.get("LUMOS_ITERATION", "0"))

    try:
        from lumos import Device  # type: ignore[import-not-found]

        device = Device.from_env()
    except Exception:  # pragma: no cover — fallback when helper missing

        class _D:
            id = os.environ.get("LUMOS_DEVICE_ID", "")
            platform = os.environ.get("LUMOS_PLATFORM", "")
            app_id = os.environ.get("LUMOS_APP_ID", "")
            iteration = iteration

        device = _D()  # type: ignore[assignment]

    try:
        mod = _load(argv[1])
    except Exception as exc:
        _emit("lumos.log", level="error", msg=f"load scenario: {exc}")
        traceback.print_exc(file=sys.stderr)
        return 1

    # Optionally auto-create an Appium driver and inject it into run().
    # Enabled when `app.appium` is set in the YAML config, which causes the
    # Go side to export LUMOS_APPIUM_AUTO=1 plus optional LUMOS_APPIUM_CAPS
    # (JSON) and LUMOS_APPIUM_URL.
    auto_driver = None
    driver_ctx = None
    if os.environ.get("LUMOS_APPIUM_AUTO") == "1":
        try:
            import json as _json

            from lumos.appium import session as _session  # type: ignore

            caps_raw = os.environ.get("LUMOS_APPIUM_CAPS", "")
            caps = _json.loads(caps_raw) if caps_raw else {}
            # Parallel UiAutomator2 sessions on the same Appium server MUST
            # use distinct per-session ports, otherwise one session binds
            # the port and the other's calls get silently routed to the
            # wrong device ("element not found" even though it's clearly
            # on screen) or the driver-create itself fails with a bind
            # error. Derive stable per-device ports so two devices never
            # collide; we hash device.id so the same device gets the same
            # port across runs (predictable for debugging).
            if device.platform == "android":
                import zlib as _zlib
                slot = _zlib.crc32(device.id.encode()) % 100
                # Three disjoint port ranges so the three values never
                # collide with each other.
                if "systemPort" not in caps:
                    caps["systemPort"] = 8200 + slot
                if "mjpegServerPort" not in caps:
                    caps["mjpegServerPort"] = 8400 + slot
                if "chromedriverPort" not in caps:
                    caps["chromedriverPort"] = 8600 + slot
            # Stagger driver-create across parallel devices so two
            # sessions don't hit Appium's session-create endpoint at
            # the same millisecond (which has caused intermittent
            # "Could not start a new session" errors).
            if device.platform == "android":
                import time as _time
                import zlib as _zlib2
                jitter_ms = _zlib2.crc32(device.id.encode()) % 750
                _time.sleep(jitter_ms / 1000.0)
            print(
                f"[lumos.harness] appium caps for {device.id}: {caps}",
                file=sys.stderr,
                flush=True,
            )
            driver_ctx = _session(device, caps=caps)
            auto_driver = driver_ctx.__enter__()
            print(
                f"[lumos.harness] driver opened for {device.id}: "
                f"session_id={getattr(auto_driver, 'session_id', '?')}",
                file=sys.stderr,
                flush=True,
            )
        except Exception as exc:
            _emit(
                "lumos.log",
                level="error",
                msg=f"appium auto-driver failed: {exc}",
            )
            traceback.print_exc(file=sys.stderr)
            return 1

    rc = 0
    iter_mode = os.environ.get("LUMOS_ITER_MODE", "")
    iter_total = int(os.environ.get("LUMOS_ITER_TOTAL", "0") or "0")
    iter_warmup = int(os.environ.get("LUMOS_ITER_WARMUP", "0") or "0")
    iter_timebox_ms = int(os.environ.get("LUMOS_ITER_TIMEBOX_MS", "0") or "0")

    def _call_phase(phase: str, it: int = 0) -> int:
        fn = getattr(mod, phase, None)
        if fn is None:
            return 0
        try:
            if phase == "run":
                if auto_driver is not None:
                    fn(device, auto_driver, it)
                else:
                    fn(device, it)
            else:
                if auto_driver is not None:
                    fn(device, auto_driver)
                else:
                    fn(device)
        except Exception as exc:
            _emit("lumos.log", level="error", msg=f"{phase} failed: {exc}")
            traceback.print_exc(file=sys.stderr)
            return 1
        return 0

    try:
        if iter_mode == "in-process" and iter_total > 0:
            # Run setup() once, then loop run() N times inside the
            # same subprocess so the Appium session is reused across
            # iterations. The Go runner slices samples by the
            # iterStart/iterEnd markers we emit here.
            rc = _call_phase("setup")
            if rc == 0:
                import time as _time
                measured_start = None
                idx = 0
                while True:
                    warmup = idx < iter_warmup
                    if warmup:
                        it_num = idx + 1
                    else:
                        it_num = idx - iter_warmup + 1
                        if measured_start is None:
                            measured_start = _time.monotonic()
                    # Stop condition: ran configured measured
                    # iterations AND (no timebox OR timebox elapsed).
                    if not warmup and it_num > iter_total - iter_warmup:
                        if iter_timebox_ms <= 0:
                            break
                        elapsed_ms = (_time.monotonic() - measured_start) * 1000
                        if elapsed_ms >= iter_timebox_ms:
                            break
                    label = f"{it_num}" + (":warmup" if warmup else "")
                    _emit("lumos.iterStart", label=label)
                    try:
                        sub_rc = _call_phase("run", it_num)
                    finally:
                        _emit("lumos.iterEnd", label=label)
                    if sub_rc != 0:
                        rc = sub_rc
                        break
                    idx += 1
            # teardown always runs, even after a failure, so the app
            # gets a chance to clean up.
            td_rc = _call_phase("teardown")
            if rc == 0:
                rc = td_rc
        else:
            # Default model: one subprocess per iteration; runner
            # spawns us once per iteration with LUMOS_ITERATION set.
            for phase in ("setup", "run", "teardown"):
                sub_rc = _call_phase(phase, iteration)
                if sub_rc != 0:
                    rc = sub_rc
                    break
    finally:
        if driver_ctx is not None:
            # With noReset=true / autoLaunch=false the UiAutomator2 driver
            # leaves the app running on driver.quit(). By default we
            # explicitly terminate it so the device returns to a clean
            # state between runs. Set
            # `app.appium.terminate_on_quit: false` in the YAML config to
            # keep the app foregrounded (useful when the next scenario
            # should resume in-process state like a PIN entry).
            terminate = os.environ.get(
                "LUMOS_APPIUM_TERMINATE_ON_QUIT", "1"
            ) != "0"
            if (
                terminate
                and auto_driver is not None
                and device.platform == "android"
            ):
                app_id = os.environ.get("LUMOS_APP_ID") or getattr(
                    device, "app_id", ""
                )
                if app_id:
                    try:
                        auto_driver.terminate_app(app_id)
                    except Exception:
                        pass
            try:
                driver_ctx.__exit__(None, None, None)
            except Exception:
                pass
    return rc


if __name__ == "__main__":
    sys.exit(main(sys.argv))
