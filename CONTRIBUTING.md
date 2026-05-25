# Contributing to Lumos

Thanks for your interest! Lumos is in pre-alpha.

## Dev setup

- Go 1.22+
- `golangci-lint` (`brew install golangci-lint`)
- Android: `adb` on PATH
- iOS (macOS only): Xcode 15+, `idb` (`brew tap facebook/fb && brew install idb-companion`)

## Workflow

1. Read [PLAN.md](PLAN.md) and pick an unblocked task from [TRACKER.md](TRACKER.md).
2. Mark the task `[~]` in TRACKER and open a draft PR early.
3. `make test lint` must pass.
4. Update PLAN/TRACKER in the same PR if scope shifts.

## Commit style

Conventional commits preferred: `feat(scheduler): add work-stealing deque`.
