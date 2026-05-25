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

    for phase in ("setup", "run", "teardown"):
        fn = getattr(mod, phase, None)
        if fn is None:
            continue
        try:
            if phase == "run":
                fn(device, iteration)
            else:
                fn(device)
        except Exception as exc:
            _emit("lumos.log", level="error", msg=f"{phase} failed: {exc}")
            traceback.print_exc(file=sys.stderr)
            return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
