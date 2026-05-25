// Package device defines the cross-platform device abstraction used by Lumos.
package device

import (
	"context"

	"github.com/dsetiawan230294/lumos/internal/metrics"
)

// Device is a handle to a single physical or virtual mobile device.
//
// Implementations live under internal/device/android and internal/device/ios.
// All methods must be safe to call from a single goroutine; concurrent use
// across goroutines is the caller's responsibility.
type Device interface {
	// ID returns the stable identifier (serial, UDID).
	ID() string
	// Platform reports android or ios.
	Platform() metrics.Platform
	// Model returns a human-friendly descriptor (e.g. "Pixel 7 / API 33").
	Model() string

	// Install installs/updates the given app artifact (apk or .ipa/.app).
	Install(ctx context.Context, artifact string) error
	// Uninstall removes the app by id.
	Uninstall(ctx context.Context, appID string) error

	// StartApp launches the app and returns its OS pid.
	StartApp(ctx context.Context, appID string) (int, error)
	// StopApp force-stops the app.
	StopApp(ctx context.Context, appID string) error

	// Collect starts a streaming metric collector for the given pid.
	// The returned channel is closed when ctx is cancelled or the device errors.
	Collect(ctx context.Context, pid int) (<-chan metrics.Sample, error)
}

// Discoverer enumerates attached devices for a single platform.
type Discoverer interface {
	Discover(ctx context.Context) ([]Device, error)
}
