package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSnapshotElements(t *testing.T) {
	raw, _ := json.Marshal([]snapEl{
		{Ref: 0, Role: "link", Name: "Home", Selector: "a.nav"},
		{Ref: 1, Role: "button", Name: "Go", Selector: "#go"},
	})
	els, err := snapshotElements(func(string) (string, error) { return string(raw), nil })
	if err != nil || len(els) != 2 || els[1].Selector != "#go" {
		t.Fatalf("snapshot = %+v, %v", els, err)
	}
	if _, err := snapshotElements(func(string) (string, error) { return "not json", nil }); err == nil {
		t.Error("malformed snapshot should error")
	}
}

func TestFormatSnapshot(t *testing.T) {
	out := formatSnapshot("https://x.test/", []snapEl{{Ref: 0, Role: "button", Name: "Go", Selector: "#go"}})
	for _, want := range []string{"# page: https://x.test/", `[0] button "Go" — #go`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// Selects surface their current choice as the name (snapshotJS reads
	// the selected option), so agents can verify a select step committed.
	out = formatSnapshot("https://x.test/", []snapEl{{Ref: 1, Role: "select", Name: "T10 (4378 paradas, 151 dias, 449.8 km)", Selector: "#fterr"}})
	if !strings.Contains(out, `[1] select "T10 (4378 paradas, 151 dias, 449.8 km)" — #fterr`) {
		t.Errorf("select choice missing:\n%s", out)
	}
}

func TestResolveRef(t *testing.T) {
	els := []snapEl{{Ref: 0, Role: "a", Name: "x", Selector: "#a"}, {Ref: 1, Role: "b", Name: "y", Selector: "#b"}}
	if got := resolveRef("1", els); got != "#b" {
		t.Errorf("ref 1 = %q", got)
	}
	for _, keep := range []string{"#q", "9", "-1", ""} {
		if got := resolveRef(keep, els); got != keep {
			t.Errorf("resolveRef(%q) = %q, want passthrough", keep, got)
		}
	}
}

func TestSessionRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	s := &browserSession{Name: "work", Target: "T1", Profile: "p"}
	if err := saveSession(s); err != nil {
		t.Fatal(err)
	}
	back, err := loadSession("work")
	if err != nil || back.Target != "T1" || back.Profile != "p" {
		t.Fatalf("round-trip = %+v, %v", back, err)
	}
	removeSession("work")
	if _, err := loadSession("work"); err == nil {
		t.Error("removed session should be gone")
	}
	if _, err := loadSession("ghost"); err == nil {
		t.Error("missing session should error")
	}
	if _, err := sessionPath("../evil"); err == nil {
		t.Error("path escape should fail validation")
	}
}
