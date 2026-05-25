# lumos-py

Python helper for authoring [Lumos](https://github.com/dsetiawan230294/lumos) mobile benchmark scenarios.

```bash
pip install lumos-py
```

## Usage

```python
from lumos import Device, mark_start, mark_end, log

device = Device.from_env()

def setup(d):
    log("info", f"setting up on {d.id} ({d.platform})")

def run(d, iteration: int):
    mark_start("scroll")
    # ... drive the app via Appium / uiautomator2 / XCUITest ...
    mark_end("scroll")

def teardown(d):
    pass
```

You don't have to install this package if you only use the Lumos CLI — the CLI
ships a vendored copy and auto-injects it into `PYTHONPATH` when needed. Install
from PyPI when you want a pinned, type-checkable dependency in CI.
