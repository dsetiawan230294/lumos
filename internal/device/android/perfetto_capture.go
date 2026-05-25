package android

import "context"

// PerfettoCapture adapts a Perfetto trace session to runner.TraceCapture. It
// is constructed lazily — Start dials adb and pushes the config; if Start
// fails (e.g. perfetto missing on a pre-Android-9 device) the caller treats
// the trace as best-effort and continues without it.
type PerfettoCapture struct {
	adb     *ADB
	serial  string
	cfg     string // text config; "" → DefaultPerfettoConfig
	session *PerfettoSession
}

// NewPerfettoCapture builds a capture bound to one device+adb pair.
func NewPerfettoCapture(adb *ADB, serial, cfg string) *PerfettoCapture {
	return &PerfettoCapture{adb: adb, serial: serial, cfg: cfg}
}

// Kind implements runner.TraceCapture.
func (p *PerfettoCapture) Kind() string { return "perfetto" }

// Start implements runner.TraceCapture.
func (p *PerfettoCapture) Start(ctx context.Context) error {
	var sess *PerfettoSession
	var err error
	if p.cfg == "" {
		sess, err = p.adb.StartPerfetto(ctx, p.serial)
	} else {
		sess, err = p.adb.StartPerfettoCustom(ctx, p.serial, p.cfg)
	}
	if err != nil {
		return err
	}
	p.session = sess
	return nil
}

// StopAndPull implements runner.TraceCapture.
func (p *PerfettoCapture) StopAndPull(ctx context.Context, localPath string) error {
	if p.session == nil {
		return nil
	}
	if err := p.session.Stop(ctx); err != nil {
		return err
	}
	return p.session.Pull(ctx, localPath)
}
