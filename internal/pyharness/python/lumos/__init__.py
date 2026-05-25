"""Lumos Python helper for authoring benchmark scenarios.

A scenario module should expose three top-level functions::

    def setup(device):    ...
    def run(device, iteration: int):  ...
    def teardown(device): ...

Inside ``run`` you may call :func:`mark_start` / :func:`mark_end` / :func:`mark`
to annotate the timeline, and :func:`log` for structured log lines. These calls
emit line-delimited JSON-RPC notifications to the Lumos host process over
stdout (the host attaches to the subprocess's stdio).

The helper is published as ``lumos-py`` on PyPI **and** vendored inside the
Lumos CLI binary. If the import is missing, the CLI auto-injects this package
into ``PYTHONPATH`` before spawning the scenario subprocess.
"""

from __future__ import annotations

from .rpc import attach, log, mark, mark_end, mark_start
from .device import Device

__all__ = ["Device", "attach", "log", "mark", "mark_end", "mark_start"]
__version__ = "0.0.1"
