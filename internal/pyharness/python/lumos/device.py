"""Read-only view of the device the host has assigned to this scenario run."""

from __future__ import annotations

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Device:
    """Identity of the device this scenario is executing against."""

    id: str
    platform: str  # "android" | "ios"
    app_id: str
    iteration: int

    @classmethod
    def from_env(cls) -> "Device":
        return cls(
            id=os.environ.get("LUMOS_DEVICE_ID", ""),
            platform=os.environ.get("LUMOS_PLATFORM", ""),
            app_id=os.environ.get("LUMOS_APP_ID", ""),
            iteration=int(os.environ.get("LUMOS_ITERATION", "0")),
        )
