package main

// Plugins are site knowledge as files, not core code. A plugin is a
// directory of JS: state.js (tab -> state JSON), tap.js (seconds ->
// {audio, diag}), bid.js (max -> result), parse.js (state, transcript
// -> verdict). Every file is invoked as an expression and must resolve
// to a JSON string (state.js and parse.js are self-invoking, tap.js and
// bid.js are called with their argument). Core only interprets: eval,
// screenshot, audio tap, transcribe. Anything naming a site's
// selectors, prices, or verdict rules lives here, never in *.go.
//
// Resolution: $PICU_PLUGIN_DIR/<name>/*.js wins when present (iterate
// without rebuilding); otherwise the copy embedded below ships it.

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed plugins/*/*.js
var pluginFS embed.FS

// pluginJS returns one plugin file, disk override first.
func pluginJS(name, file string) string {
	if dir := os.Getenv("PICU_PLUGIN_DIR"); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, name, file)); err == nil {
			return string(b)
		}
	}
	b, err := pluginFS.ReadFile("plugins/" + name + "/" + file)
	if err != nil {
		fatal(fmt.Sprintf("plugin %q has no %s", name, file))
	}
	return string(b)
}

// pluginHas reports whether the plugin ships the file.
func pluginHas(name, file string) bool {
	if dir := os.Getenv("PICU_PLUGIN_DIR"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, name, file)); err == nil {
			return true
		}
	}
	_, err := pluginFS.ReadFile("plugins/" + name + "/" + file)
	return err == nil
}

// runEval implements `picu eval --session S 'JS'`: the generic plugin
// interface — run any expression in the tab, print the raw result.
func runEval(js string, o fetchOpts) {
	name := *sessionFlg
	s, err := loadSession(name)
	if err != nil {
		fatal(err.Error())
	}
	_, eval, cancelTask, cancelAlloc, cancelParent, err := sessionCtx(s, o)
	if err != nil {
		fatal(err.Error())
	}
	defer cancelParent()
	defer cancelAlloc()
	defer cancelTask()
	raw, err := eval(js)
	if err != nil {
		fatal(err.Error())
	}
	fmt.Println(raw)
}
