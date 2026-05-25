package config

import (
	"path/filepath"
	"testing"
)

func TestLoadValid(t *testing.T) {
	c, err := Load(filepath.Join("testdata", "valid.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.App.Android != "com.example.app" {
		t.Errorf("android app id = %q, want com.example.app", c.App.Android)
	}
	if len(c.Scenarios) != 1 {
		t.Fatalf("scenarios len = %d, want 1", len(c.Scenarios))
	}
	if c.Scenarios[0].Iterations != 10 {
		t.Errorf("iterations = %d, want 10", c.Scenarios[0].Iterations)
	}
}

func TestValidateRequiresApp(t *testing.T) {
	c := &Config{Scenarios: []Scenario{{Name: "x", Script: "s.py", Iterations: 1}}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error when no app id provided")
	}
}

func TestValidateRequiresScenario(t *testing.T) {
	c := &Config{App: App{Android: "com.example"}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error when no scenarios provided")
	}
}

func TestValidateHookSkipsIterationCheck(t *testing.T) {
	// A hook scenario does not need iterations/warmup; validation must pass.
	c := &Config{
		App: App{Android: "com.example"},
		Scenarios: []Scenario{
			{Name: "login", Script: "login.py", Hook: true},
			{Name: "scroll", Script: "scroll.py", Iterations: 3},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateNegativeTimebox(t *testing.T) {
	c := &Config{
		App: App{Android: "com.example"},
		Scenarios: []Scenario{
			{Name: "x", Script: "s.py", Iterations: 1, Timebox: -1},
		},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for negative timebox")
	}
}
