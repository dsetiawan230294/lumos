// Package budget evaluates a YAML "perf budget" — per-metric absolute
// thresholds — against a directory of Lumos run reports.
//
// Unlike `lumos compare`, which detects relative regressions vs a baseline
// run, budget checks let CI assert absolute targets ("p90 fps must be >= 55")
// without needing any prior artifact. The two are complementary.
package budget

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/dsetiawan230294/lumos/internal/report"
	"gopkg.in/yaml.v3"
)

// Budget is the parsed perf-budget file.
//
// YAML shape:
//
//	default:
//	  fps:       { p90: ">= 55", mean: ">= 58" }
//	  frame_ms:  { p90: "<= 18" }
//	  cpu_pct:   { mean: "<= 30" }
//	scenarios:
//	  scroll_feed:
//	    fps: { p90: ">= 58" }   # tighter than default
//
// Per-scenario rules override the default for the same (metric, stat) pair.
// Other rules from `default` still apply.
type Budget struct {
	Default   map[string]map[string]string            `yaml:"default,omitempty"`
	Scenarios map[string]map[string]map[string]string `yaml:"scenarios,omitempty"`
}

// Rule is one parsed assertion: a metric-stat pair, an operator, and a value.
type Rule struct {
	Metric string
	Stat   string
	Op     string // "<=", ">=", "<", ">", "=="
	Value  float64
}

func (r Rule) String() string {
	return fmt.Sprintf("%s.%s %s %v", r.Metric, r.Stat, r.Op, r.Value)
}

// Violation is one failed rule against one run.
type Violation struct {
	Scenario  string  `json:"scenario"`
	DeviceID  string  `json:"device_id"`
	Iteration int     `json:"iteration"`
	Rule      Rule    `json:"rule"`
	Actual    float64 `json:"actual"`
	Reason    string  `json:"reason"` // human-readable
}

// Result bundles all violations from a budget check.
type Result struct {
	Budget     string      `json:"budget"`
	Dir        string      `json:"dir"`
	Rules      int         `json:"rules_evaluated"`
	Violations []Violation `json:"violations"`
	Pass       bool        `json:"pass"`
}

// Load reads and parses a YAML budget file.
func Load(path string) (*Budget, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bg Budget
	if err := yaml.Unmarshal(b, &bg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// Validate every rule eagerly so users get errors up front.
	for metric, stats := range bg.Default {
		for stat, expr := range stats {
			if _, err := parseRule(metric, stat, expr); err != nil {
				return nil, fmt.Errorf("default.%s.%s: %w", metric, stat, err)
			}
		}
	}
	for sc, metrics := range bg.Scenarios {
		for metric, stats := range metrics {
			for stat, expr := range stats {
				if _, err := parseRule(metric, stat, expr); err != nil {
					return nil, fmt.Errorf("scenarios.%s.%s.%s: %w", sc, metric, stat, err)
				}
			}
		}
	}
	return &bg, nil
}

// rulesFor returns the effective rule set for one scenario, with per-scenario
// overrides taking precedence over the default.
func (bg *Budget) rulesFor(scenario string) []Rule {
	merged := map[string]map[string]string{}
	for m, stats := range bg.Default {
		merged[m] = map[string]string{}
		for s, e := range stats {
			merged[m][s] = e
		}
	}
	for m, stats := range bg.Scenarios[scenario] {
		if merged[m] == nil {
			merged[m] = map[string]string{}
		}
		for s, e := range stats {
			merged[m][s] = e
		}
	}
	var out []Rule
	for m, stats := range merged {
		for s, e := range stats {
			r, err := parseRule(m, s, e)
			if err != nil {
				continue // already validated in Load
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Metric != out[j].Metric {
			return out[i].Metric < out[j].Metric
		}
		return out[i].Stat < out[j].Stat
	})
	return out
}

// Check walks reports and evaluates the budget against each. Reports whose
// scenario has no rules (and no defaults) are ignored.
func (bg *Budget) Check(reports []report.RunReport) Result {
	res := Result{Pass: true}
	// Stable order: scenario, device, iteration.
	sort.SliceStable(reports, func(i, j int) bool {
		a, b := reports[i].Run, reports[j].Run
		if a.Scenario != b.Scenario {
			return a.Scenario < b.Scenario
		}
		if a.DeviceID != b.DeviceID {
			return a.DeviceID < b.DeviceID
		}
		return a.Iteration < b.Iteration
	})
	for _, r := range reports {
		rules := bg.rulesFor(r.Run.Scenario)
		res.Rules += len(rules)
		for _, rule := range rules {
			summary, ok := r.Summary[rule.Metric]
			if !ok || summary.Count == 0 {
				continue // metric not present in this run — skip silently
			}
			actual := pickStat(summary, rule.Stat)
			if !satisfies(actual, rule) {
				res.Violations = append(res.Violations, Violation{
					Scenario:  r.Run.Scenario,
					DeviceID:  r.Run.DeviceID,
					Iteration: r.Run.Iteration,
					Rule:      rule,
					Actual:    actual,
					Reason: fmt.Sprintf("%s.%s = %s, want %s %v",
						rule.Metric, rule.Stat, trimFloat(actual), rule.Op, rule.Value),
				})
				res.Pass = false
			}
		}
	}
	return res
}

// CheckDir loads a results directory and runs Check.
func (bg *Budget) CheckDir(dir string) (Result, error) {
	reps, _, err := report.LoadRunReports(dir)
	if err != nil {
		return Result{}, err
	}
	res := bg.Check(reps)
	res.Dir = dir
	return res, nil
}

// FormatText renders a human-readable summary.
func (r Result) FormatText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "budget: %s\ndir:    %s\nrules:  %d evaluated\n\n", r.Budget, r.Dir, r.Rules)
	if len(r.Violations) == 0 {
		fmt.Fprintln(&b, "PASS — no budget violations.")
		return b.String()
	}
	fmt.Fprintf(&b, "FAIL — %d violation(s):\n\n", len(r.Violations))
	for _, v := range r.Violations {
		fmt.Fprintf(&b, "  %s/%s#%d  %s\n", v.Scenario, v.DeviceID, v.Iteration, v.Reason)
	}
	return b.String()
}

// ---- internals ----

var validOps = map[string]bool{
	"<=": true, ">=": true, "<": true, ">": true, "==": true,
}

func parseRule(metric, stat, expr string) (Rule, error) {
	expr = strings.TrimSpace(expr)
	// Match a 1- or 2-char operator prefix.
	var op string
	switch {
	case strings.HasPrefix(expr, "<="), strings.HasPrefix(expr, ">="), strings.HasPrefix(expr, "=="):
		op = expr[:2]
	case strings.HasPrefix(expr, "<"), strings.HasPrefix(expr, ">"):
		op = expr[:1]
	default:
		return Rule{}, fmt.Errorf("missing operator in %q (want one of <=, >=, <, >, ==)", expr)
	}
	if !validOps[op] {
		return Rule{}, fmt.Errorf("invalid operator %q", op)
	}
	rest := strings.TrimSpace(strings.TrimPrefix(expr, op))
	v, err := strconv.ParseFloat(rest, 64)
	if err != nil {
		return Rule{}, fmt.Errorf("invalid number %q: %w", rest, err)
	}
	if !validStat(stat) {
		return Rule{}, fmt.Errorf("unknown stat %q (want mean|p50|p90|p99|min|max)", stat)
	}
	return Rule{Metric: metric, Stat: stat, Op: op, Value: v}, nil
}

func validStat(s string) bool {
	switch s {
	case "mean", "p50", "p90", "p99", "min", "max":
		return true
	}
	return false
}

func pickStat(s report.Summary, stat string) float64 {
	switch stat {
	case "mean":
		return s.Mean
	case "p50":
		return s.P50
	case "p90":
		return s.P90
	case "p99":
		return s.P99
	case "min":
		return s.Min
	case "max":
		return s.Max
	}
	return 0
}

func satisfies(actual float64, r Rule) bool {
	switch r.Op {
	case "<=":
		return actual <= r.Value
	case ">=":
		return actual >= r.Value
	case "<":
		return actual < r.Value
	case ">":
		return actual > r.Value
	case "==":
		return actual == r.Value
	}
	return false
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}
