package doctor

import (
	"bytes"
	"strings"
	"testing"
)

func TestStatusString(t *testing.T) {
	cases := map[Status]string{OK: "OK", Warn: "WARN", Fail: "FAIL", Skip: "SKIP"}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("%d.String()=%q, want %q", s, got, want)
		}
	}
}

func TestReportAddCounters(t *testing.T) {
	var r Report
	r.add(Check{Name: "a", Status: OK})
	r.add(Check{Name: "b", Status: Warn})
	r.add(Check{Name: "c", Status: Fail})
	r.add(Check{Name: "d", Status: Skip})
	r.add(Check{Name: "e", Status: OK})
	if r.OK != 2 || r.Warn != 1 || r.Fail != 1 || r.Skip != 1 {
		t.Errorf("counters wrong: %+v", r)
	}
}

func TestReportVerdict(t *testing.T) {
	tests := []struct {
		name string
		r    Report
		want Status
	}{
		{"all ok", Report{OK: 3}, OK},
		{"warn beats ok", Report{OK: 1, Warn: 1}, Warn},
		{"fail beats warn", Report{OK: 1, Warn: 1, Fail: 1}, Fail},
		{"fail beats all", Report{Fail: 1}, Fail},
		{"empty is ok", Report{}, OK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Verdict(); got != tc.want {
				t.Errorf("Verdict()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestRender_IncludesAllChecksAndSummary(t *testing.T) {
	r := Report{
		GoVersion: "go1.25.0",
		GOOS:      "darwin",
		GOARCH:    "arm64",
		Checks: []Check{
			{Name: "adb", Status: OK, Detail: "Android Debug Bridge 35.0.2"},
			{Name: "android devices", Status: Warn, Detail: "1 unauthorized", Hint: "accept the prompt"},
			{Name: "xcrun", Status: Fail, Detail: "not found on PATH", Hint: "install Xcode CLT"},
			{Name: "ios devices", Status: Skip, Detail: "xcrun missing"},
		},
		OK: 1, Warn: 1, Fail: 1, Skip: 1,
	}

	var buf bytes.Buffer
	Render(&buf, r)
	s := buf.String()

	for _, want := range []string{
		"darwin/arm64", "go1.25.0",
		"adb", "Android Debug Bridge",
		"android devices", "1 unauthorized", "accept the prompt",
		"xcrun", "not found", "install Xcode CLT",
		"ios devices",
		"1 ok · 1 warn · 1 fail · 1 skip",
		"blocked:",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Render output missing %q\n--full output--\n%s", want, s)
		}
	}
}

func TestRender_SkipsHintForOKChecks(t *testing.T) {
	r := Report{
		Checks: []Check{{Name: "adb", Status: OK, Detail: "v35", Hint: "shouldn't appear"}},
		OK:     1,
	}
	var buf bytes.Buffer
	Render(&buf, r)
	if strings.Contains(buf.String(), "shouldn't appear") {
		t.Errorf("hint must not be rendered for OK checks: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "ready to roll") {
		t.Errorf("OK verdict line missing: %s", buf.String())
	}
}

func TestRender_WarnVerdictLine(t *testing.T) {
	r := Report{Checks: []Check{{Name: "adb", Status: Warn, Detail: "x"}}, Warn: 1}
	var buf bytes.Buffer
	Render(&buf, r)
	if !strings.Contains(buf.String(), "optional features") {
		t.Errorf("warn verdict line missing: %s", buf.String())
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"single":              "single",
		"first\nsecond":       "first",
		"\n  leading\nnext":   "leading",
		"   \n":               "",
		"only-trailing-nl\n":  "only-trailing-nl",
		"with\nmany\nlines\n": "with",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q)=%q, want %q", in, got, want)
		}
	}
}
