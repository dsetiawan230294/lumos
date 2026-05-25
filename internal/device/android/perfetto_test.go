package android

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStartPerfetto_BuildsCommandAndParsesTag(t *testing.T) {
	fe := newFakeExec(t)
	var capturedShell []string
	fe.on("shell", func(args []string) ([]byte, []byte, error) {
		capturedShell = append([]string(nil), args...)
		// Perfetto with --detach prints the detach key + a banner on success.
		return []byte("perfetto: 2024-01-01 starting tracing session\n"), nil, nil
	})
	adb := NewADB(WithExecer(fe))

	sess, err := adb.StartPerfetto(context.Background(), "ABCD1234")
	if err != nil {
		t.Fatalf("StartPerfetto: %v", err)
	}
	if sess == nil || sess.tag == "" || sess.devicePath == "" {
		t.Fatalf("session not populated: %+v", sess)
	}
	if !strings.HasPrefix(sess.tag, "lumos-") {
		t.Fatalf("unexpected tag: %s", sess.tag)
	}
	if !strings.HasSuffix(sess.devicePath, ".perfetto-trace") {
		t.Fatalf("unexpected device path: %s", sess.devicePath)
	}
	// The shell invocation should run perfetto via heredoc with --detach=<tag>.
	joined := strings.Join(capturedShell, " ")
	if !strings.Contains(joined, "perfetto -c - --txt") {
		t.Fatalf("missing perfetto cmd:\n%s", joined)
	}
	if !strings.Contains(joined, "--detach="+sess.tag) {
		t.Fatalf("missing --detach=%s in: %s", sess.tag, joined)
	}
	if !strings.Contains(joined, sess.devicePath) {
		t.Fatalf("missing remote path %s in: %s", sess.devicePath, joined)
	}
}

func TestStartPerfetto_DetectsMissingBinary(t *testing.T) {
	fe := newFakeExec(t)
	fe.on("shell", func(args []string) ([]byte, []byte, error) {
		return []byte("/system/bin/sh: perfetto: not found\n"), nil, nil
	})
	adb := NewADB(WithExecer(fe))
	if _, err := adb.StartPerfetto(context.Background(), "X"); err == nil {
		t.Fatal("expected error when perfetto is not on device")
	}
}

func TestStartPerfetto_SurfacesAdbError(t *testing.T) {
	fe := newFakeExec(t)
	fe.on("shell", func(args []string) ([]byte, []byte, error) {
		return nil, nil, errors.New("adb: device offline")
	})
	adb := NewADB(WithExecer(fe))
	if _, err := adb.StartPerfetto(context.Background(), "X"); err == nil {
		t.Fatal("expected error when adb fails")
	}
}

func TestPerfettoSession_StopIssuesAttachStop(t *testing.T) {
	fe := newFakeExec(t)
	var shellArgs [][]string
	fe.on("shell", func(args []string) ([]byte, []byte, error) {
		shellArgs = append(shellArgs, append([]string(nil), args...))
		return []byte("ok\n"), nil, nil
	})
	adb := NewADB(WithExecer(fe))

	sess := &PerfettoSession{adb: adb, serial: "S1", tag: "lumos-42", devicePath: "/data/misc/perfetto-traces/lumos-42.perfetto-trace"}
	if err := sess.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(shellArgs) != 1 {
		t.Fatalf("expected 1 shell call, got %d", len(shellArgs))
	}
	joined := strings.Join(shellArgs[0], " ")
	if !strings.Contains(joined, "perfetto --attach=lumos-42 --stop") {
		t.Fatalf("missing attach+stop, got: %s", joined)
	}
}

func TestPerfettoSession_StopNilIsNoop(t *testing.T) {
	var sess *PerfettoSession
	if err := sess.Stop(context.Background()); err != nil {
		t.Fatalf("nil Stop returned err: %v", err)
	}
}
