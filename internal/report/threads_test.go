package report

import "testing"

func TestNormalizeThreadName(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"RenderThread":    "RenderThread",
		"binder:11840_1":  "binder:*_*",
		"binder:10678_4":  "binder:*_*",
		"HwBinder:228_3":  "HwBinder:*_*",
		"pool-1-thread-4": "pool-*-thread-*",
		"mali-cmar-backe": "mali-cmar-backe",
		"GLThread 123":    "GLThread *",
		"123abc456":       "*abc*",
		"abc":             "abc",
		"42":              "*",
	}
	for in, want := range cases {
		if got := normalizeThreadName(in); got != want {
			t.Errorf("normalizeThreadName(%q) = %q, want %q", in, got, want)
		}
	}
}
