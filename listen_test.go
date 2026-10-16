package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The verdict rules live in plugins/, not Go: these tests pin the
// contract (which files each command needs) instead of the rules.
func TestPluginFilesEmbedded(t *testing.T) {
	for _, f := range []string{"state.js", "tap.js", "bid.js", "parse.js"} {
		body := pluginJS("whatnot", f)
		if len(body) < 50 {
			t.Errorf("whatnot/%s suspiciously short", f)
		}
	}
	if body := pluginJS("default", "tap.js"); !strings.Contains(body, "MediaRecorder") {
		t.Error("default/tap.js should record page media")
	}
	if body := pluginJS("whatnot", "state.js"); !strings.Contains(body, "chat-message") {
		t.Error("whatnot/state.js should read chat rows")
	}
}

func TestPluginTapFallback(t *testing.T) {
	own := pluginJS("whatnot", "tap.js")
	if !strings.Contains(own, "__ARTC__") {
		t.Error("whatnot tap should prefer the SDK track")
	}
	// A plugin without tap.js falls back to the generic element tap.
	if got := pluginTap("whatnot"); got != own {
		t.Error("plugin with tap.js should use its own")
	}
	if got := pluginTap("no-such-plugin"); got != pluginJS("default", "tap.js") {
		t.Error("plugin without tap.js should fall back to default")
	}
}

func TestPluginDirOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "whatnot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "whatnot", "state.js"), []byte("OVERRIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PICU_PLUGIN_DIR", dir)
	if got := pluginJS("whatnot", "state.js"); got != "OVERRIDE" {
		t.Errorf("disk override should win, got %q", got)
	}
}
