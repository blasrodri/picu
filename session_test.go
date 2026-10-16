package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	// Markers and popups ride along as plain entries so map pins are
	// clickable refs even though they are not controls.
	out = formatSnapshot("https://x.test/", []snapEl{
		{Ref: 5, Role: "img", Name: "image", Selector: "#map img.marker"},
		{Ref: 6, Role: "dialog", Name: "Dra. López · Pediatra · Av. Providencia 1208", Selector: ".popup"},
		{Ref: 7, Role: "div", Name: "Av. Providencia 1208", Selector: "#map div.pin"},
	})
	for _, want := range []string{`[5] img "image" — #map img.marker`, `[6] dialog "Dra. López · Pediatra · Av. Providencia 1208" — .popup`, `[7] div "Av. Providencia 1208" — #map div.pin`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
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

func TestListSessions(t *testing.T) {
	t.Setenv("PICU_CONFIG_HOME", t.TempDir())
	if err := saveSession(&browserSession{Name: "a", Target: "t1", Profile: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := saveSession(&browserSession{Name: "b", Target: "t2", Profile: "p"}); err != nil {
		t.Fatal(err)
	}
	// Corrupt record is skipped, not fatal. Write it straight into the
	// sessions dir next to the good ones.
	sp, err := sessionPath("a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(sp), "bad.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	all := listSessions()
	if len(all) != 2 {
		t.Fatalf("listSessions = %+v", all)
	}
	if err := saveSession(&browserSession{Name: "p", Target: "t3", Profile: "p", Proxy: "http://x:1"}); err != nil {
		t.Fatal(err)
	}
	all = listSessions()
	if len(all) != 3 {
		t.Fatalf("listSessions = %+v", all)
	}
	for _, s := range all {
		if s.Name == "p" && s.Proxy != "http://x:1" {
			t.Errorf("session proxy = %q, want pinned value", s.Proxy)
		}
	}
}

func TestPruneDropsGhosts(t *testing.T) {
	t.Setenv("PICU_CONFIG_HOME", t.TempDir())
	if err := saveSession(&browserSession{Name: "ghost", Target: "dead", Profile: "nope"}); err != nil {
		t.Fatal(err)
	}
	if len(listSessions()) != 1 {
		t.Fatal("setup should leave one record")
	}
	runPrune()
	if len(listSessions()) != 0 {
		t.Error("prune should drop the record whose profile/daemon is gone")
	}
}

func TestSessionDaemonDirParity(t *testing.T) {
	if got := sessionDaemonDir("/wd", ""); got != "/wd" {
		t.Errorf("direct dir = %q", got)
	}
	a := sessionDaemonDir("/wd", "http://u:p@h:1")
	b := sessionDaemonDir("/wd", "http://u:p@h:1")
	if a == "/wd" || a != b {
		t.Errorf("proxied dir should be stable and distinct: %q", a)
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
