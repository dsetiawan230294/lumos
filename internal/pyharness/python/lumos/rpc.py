"""Stdio JSON-RPC notification helpers used by scenario scripts."""

from __future__ import annotations

import json
import os
import sys
import threading
from typing import Any

_lock = threading.Lock()


def _notify(method: str, **params: Any) -> None:
    """Emit a JSON-RPC notification to the Lumos host on stdout."""
    payload = {"jsonrpc": "2.0", "method": method, "params": params}
    line = json.dumps(payload, separators=(",", ":"))
    with _lock:
        sys.stdout.write(line + "\n")
        sys.stdout.flush()


def mark_start(label: str) -> None:
    """Mark the start of a labelled segment on the timeline."""
    _notify("lumos.markStart", label=label)


def mark_end(label: str) -> None:
    """Mark the end of a labelled segment on the timeline."""
    _notify("lumos.markEnd", label=label)


def mark(label: str) -> None:
    """Drop a single point-in-time marker."""
    _notify("lumos.mark", label=label)


def log(level: str, msg: str) -> None:
    """Send a structured log line to the host."""
    _notify("lumos.log", level=level, msg=msg)


def attach(path: str) -> None:
    """Attach a file (screenshot, trace, etc.) to the current run."""
    _notify("lumos.attach", path=os.fspath(path))
