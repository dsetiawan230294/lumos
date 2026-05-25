package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsetiawan230294/lumos/internal/metrics"
	"github.com/dsetiawan230294/lumos/internal/report"
)

func writeBudget(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "budget.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func makeReport(scenario, dev string, iter int, fpsMean, fpsP90, cpuMean float64) report.RunReport {
	t0 := time.Now().UTC()
	samples := []metrics.Sample{}
	// Synthesize samples whose stats land on the requested values.
	// Three identical samples => mean == p90 == p99 == that value.
	for i := 0; i < 3; i++ {
		samples = append(samples, metrics.Sample{T: t0.Add(time.Duration(i) * time.Second), FPS: fpsMean, CPUPct: cpuMean})
	}
	// One last sample tweaked to land p90 above mean.
	samples = append(samples, metrics.Sample{T: t0.Add(3 * time.Second), FPS: fpsP90, CPUPct: cpuMean})
	r := metrics.Run{
		Scenario:  scenario,
		DeviceID:  dev,
		Platform:  metrics.Android,
		Iteration: iter,
		StartedAt: t0,
		EndedAt:   t0.Add(3 * time.Second),
		Samples:   samples,
	}
	return report.RunReport{
		Schema:  "1",
		Tool:    "lumos",
		Version: "test",
		Run:     r,
		Summary: report.SummarizeRun(r),
	}
}

func TestParseRule(t *testing.T) {
	cases := []struct {
		expr      string
		wantOp    string
		wantValue float64
		wantErr   bool
	}{
		{">= 55", ">=", 55, false},
		{"<=18", "<=", 18, false},
		{"< 100", "<", 100, false},
		{"> 0.5", ">", 0.5, false},
		{"== 60", "==", 60, false},
		{"55", "", 0, true},     // no operator
		{">= abc", "", 0, true}, // bad number
	}
	for _, c := range cases {
		r, err := parseRule("fps", "p90", c.expr)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseRule(%q): want err", c.expr)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseRule(%q): %v", c.expr, err)
			continue
		}
		if r.Op != c.wantOp || r.Value != c.wantValue {
			t.Errorf("parseRule(%q)=%v %v, want %v %v", c.expr, r.Op, r.Value, c.wantOp, c.wantValue)
		}
	}
}

func TestParseRule_InvalidStat(t *testing.T) {
	if _, err := parseRule("fps", "median", ">= 55"); err == nil {
		t.Fatal("want error for unknown stat")
	}
}

func TestLoad_ValidatesEagerly(t *testing.T) {
	dir := t.TempDir()
	p := writeBudget(t, dir, `default:
  fps:
    p90: "55"   # missing operator
`)
	if _, err := Load(p); err == nil {
		t.Fatal("want validation error for missing operator")
	}
}

func TestCheck_PassesWhenAllRulesMet(t *testing.T) {
	dir := t.TempDir()
	p := writeBudget(t, dir, `default:
  fps:
    mean: ">= 50"
    p90:  ">= 55"
  cpu_pct:
    mean: "<= 30"
`)
	bg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	reps := []report.RunReport{makeReport("scroll", "D1", 1, 60, 65, 20)}
	res := bg.Check(reps)
	if !res.Pass {
		t.Fatalf("expected PASS, got violations: %+v", res.Violations)
	}
	if res.Rules == 0 {
		t.Errorf("rules evaluated=0, want >0")
	}
}

func TestCheck_DetectsHigherIsBetterViolation(t *testing.T) {
	dir := t.TempDir()
	p := writeBudget(t, dir, `default:
  fps:
    p90: ">= 60"
`)
	bg, _ := Load(p)
	reps := []report.RunReport{makeReport("scroll", "D1", 1, 55, 58, 20)} // p90=58 < 60
	res := bg.Check(reps)
	if res.Pass {
		t.Fatal("expected FAIL")
	}
	if len(res.Violations) != 1 {
		t.Fatalf("violations=%d, want 1", len(res.Violations))
	}
	v := res.Violations[0]
	if v.Rule.Metric != "fps" || v.Rule.Op != ">=" {
		t.Errorf("unexpected rule: %+v", v.Rule)
	}
}

func TestCheck_DetectsLowerIsBetterViolation(t *testing.T) {
	dir := t.TempDir()
	p := writeBudget(t, dir, `default:
  cpu_pct:
    mean: "<= 25"
`)
	bg, _ := Load(p)
	reps := []report.RunReport{makeReport("scroll", "D1", 1, 60, 65, 40)} // cpu mean=40 > 25
	res := bg.Check(reps)
	if res.Pass || len(res.Violations) != 1 {
		t.Fatalf("expected 1 violation, got %d (pass=%v)", len(res.Violations), res.Pass)
	}
}

func TestCheck_ScenarioOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	p := writeBudget(t, dir, `default:
  fps:
    p90: ">= 50"
scenarios:
  scroll:
    fps:
      p90: ">= 70"
`)
	bg, _ := Load(p)
	reps := []report.RunReport{
		makeReport("scroll", "D1", 1, 60, 65, 20), // p90=65 fails the tighter scroll rule (>=70)
		makeReport("idle", "D1", 1, 60, 65, 20),   // uses default >=50, passes
	}
	res := bg.Check(reps)
	if res.Pass {
		t.Fatal("expected FAIL on scroll")
	}
	if len(res.Violations) != 1 {
		t.Fatalf("violations=%d, want 1 (only scroll)", len(res.Violations))
	}
	if res.Violations[0].Scenario != "scroll" {
		t.Errorf("violation scenario=%q, want scroll", res.Violations[0].Scenario)
	}
}

func TestCheck_SkipsMissingMetrics(t *testing.T) {
	dir := t.TempDir()
	p := writeBudget(t, dir, `default:
  fps: { p90: ">= 50" }
  ram_mb: { max: "<= 400" }  # ram not produced in fixture
`)
	bg, _ := Load(p)
	reps := []report.RunReport{makeReport("scroll", "D1", 1, 60, 65, 20)}
	res := bg.Check(reps)
	if !res.Pass {
		t.Fatalf("expected PASS (missing metric should be ignored), got %+v", res.Violations)
	}
}

func TestCheckDir_E2E(t *testing.T) {
	dir := t.TempDir()
	// Write a fixture JSON the same way other report tests do — round-trip
	// through report.RunReport's JSON encoding.
	rep := makeReport("scroll", "D1", 1, 60, 65, 20)
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scroll_001_D1.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	p := writeBudget(t, dir, `default:
  fps:
    p90: ">= 50"
`)
	bg, _ := Load(p)
	res, err := bg.CheckDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass {
		t.Fatalf("expected PASS: %s", res.FormatText())
	}
	if !strings.Contains(res.FormatText(), "PASS") {
		t.Errorf("FormatText missing PASS: %s", res.FormatText())
	}
}
