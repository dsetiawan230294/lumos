package android

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeExec stubs Execer for tests by routing on the first arg.
type fakeExec struct {
	t        *testing.T
	handlers map[string]func(args []string) (stdout, stderr []byte, err error)
	calls    [][]string
}

func newFakeExec(t *testing.T) *fakeExec {
	return &fakeExec{t: t, handlers: map[string]func([]string) ([]byte, []byte, error){}}
}

func (f *fakeExec) on(key string, h func(args []string) ([]byte, []byte, error)) {
	f.handlers[key] = h
}

func (f *fakeExec) Run(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, args)
	for _, a := range args {
		if h, ok := f.handlers[a]; ok {
			return h(args)
		}
	}
	f.t.Fatalf("unexpected adb invocation: %v", args)
	return nil, nil, nil
}

func TestParseDevicesL(t *testing.T) {
	in := `List of devices attached
emulator-5554  device product:sdk_gphone_x86 model:Pixel_7 device:generic_x86 transport_id:1
R3CN30XXXX     unauthorized usb:1-1
`
	got := parseDevicesL(in)
	want := []DeviceInfo{
		{
			Serial: "emulator-5554", State: "device",
			Product: "sdk_gphone_x86", Model: "Pixel 7", DeviceID: "generic_x86",
			Extra: map[string]string{"transport_id": "1"},
		},
		{
			Serial: "R3CN30XXXX", State: "unauthorized",
			Extra: map[string]string{"usb": "1-1"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDevicesL mismatch:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestDevicesEnrichesAPIAndModel(t *testing.T) {
	fe := newFakeExec(t)
	fe.on("devices", func(args []string) ([]byte, []byte, error) {
		return []byte("List of devices attached\nABCD1234 device transport_id:1\n"), nil, nil
	})
	fe.on("shell", func(args []string) ([]byte, []byte, error) {
		// args = ["-s","ABCD1234","shell","getprop","<key>"]
		key := args[len(args)-1]
		switch key {
		case "ro.build.version.sdk":
			return []byte("33\n"), nil, nil
		case "ro.product.model":
			return []byte("Pixel 7\n"), nil, nil
		}
		return nil, nil, errors.New("unexpected getprop key: " + key)
	})

	adb := NewADB(WithExecer(fe))
	infos, err := adb.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len=%d, want 1", len(infos))
	}
	if infos[0].APILevel != 33 {
		t.Errorf("APILevel=%d, want 33", infos[0].APILevel)
	}
	if infos[0].Model != "Pixel 7" {
		t.Errorf("Model=%q, want Pixel 7", infos[0].Model)
	}
}

func TestDevicesSkipsEnrichmentForOffline(t *testing.T) {
	fe := newFakeExec(t)
	fe.on("devices", func(args []string) ([]byte, []byte, error) {
		return []byte("List of devices attached\nXYZ unauthorized\n"), nil, nil
	})
	adb := NewADB(WithExecer(fe))
	infos, err := adb.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(infos) != 1 || infos[0].State != "unauthorized" {
		t.Fatalf("unexpected: %#v", infos)
	}
	if len(fe.calls) != 1 {
		t.Errorf("expected no enrichment calls, got %d", len(fe.calls)-1)
	}
}

func TestVersion(t *testing.T) {
	fe := newFakeExec(t)
	fe.on("version", func(args []string) ([]byte, []byte, error) {
		return []byte("Android Debug Bridge version 1.0.41\n"), nil, nil
	})
	adb := NewADB(WithExecer(fe))
	v, err := adb.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if !strings.Contains(v, "1.0.41") {
		t.Errorf("Version=%q", v)
	}
}
