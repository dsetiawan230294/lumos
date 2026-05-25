package android

import "testing"

func TestParseThreadStats_SingleLine(t *testing.T) {
	in := `1234 (RenderThread) S 1 1 1 0 -1 4194304 0 0 0 0 100 50 0 0 20 0 1 0 ...`
	got := parseThreadStats(in)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1", len(got))
	}
	if got[0].TID != 1234 || got[0].Comm != "RenderThread" || got[0].Jiffies != 150 {
		t.Errorf("got %+v", got[0])
	}
}

func TestParseThreadStats_MultipleConcatenated(t *testing.T) {
	in := "10 (main) S 1 1 1 0 -1 0 0 0 0 0 200 100 0 0 20 0 1 0\n" +
		"11 (RenderThread) S 1 1 1 0 -1 0 0 0 0 0 80 20 0 0 20 0 1 0\n" +
		"12 (mqt js) S 1 1 1 0 -1 0 0 0 0 0 5 5 0 0 20 0 1 0\n"
	got := parseThreadStats(in)
	if len(got) != 3 {
		t.Fatalf("len=%d want 3 (%+v)", len(got), got)
	}
	want := map[int]struct {
		comm string
		j    uint64
	}{
		10: {"main", 300},
		11: {"RenderThread", 100},
		12: {"mqt js", 10},
	}
	for _, ts := range got {
		w, ok := want[ts.TID]
		if !ok {
			t.Errorf("unexpected tid %d", ts.TID)
			continue
		}
		if ts.Comm != w.comm || ts.Jiffies != w.j {
			t.Errorf("tid %d: got (%q, %d) want (%q, %d)", ts.TID, ts.Comm, ts.Jiffies, w.comm, w.j)
		}
	}
}

func TestParseThreadStats_CommWithParensAndSpaces(t *testing.T) {
	// comm with embedded spaces and a stray ')' should still parse using
	// the rightmost-")" heuristic.
	in := "42 (weird (name)) S 1 1 1 0 -1 0 0 0 0 0 7 3 0 0 20 0 1 0\n"
	got := parseThreadStats(in)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1", len(got))
	}
	if got[0].TID != 42 || got[0].Jiffies != 10 {
		t.Errorf("got %+v", got[0])
	}
}

func TestParseThreadStats_SkipsMalformed(t *testing.T) {
	in := "garbage line without parens\n" +
		"99 (ok) S 1 1 1 0 -1 0 0 0 0 0 1 1 0 0 20 0 1 0\n"
	got := parseThreadStats(in)
	// First line is unparseable; we move past it and pick up the second.
	if len(got) < 1 {
		t.Fatalf("expected at least one parsed thread, got %d", len(got))
	}
	found := false
	for _, ts := range got {
		if ts.TID == 99 && ts.Comm == "ok" && ts.Jiffies == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected tid=99 ok 2 in %+v", got)
	}
}
