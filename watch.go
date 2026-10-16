package main

// watch streams a plugin's tab state as JSON lines: one line per
// change. Read-only DOM polls on a slow ticker — it never clicks,
// bids, or navigates. Bidding stays a separate explicit command
// (runBid), which never auto-confirms. All site knowledge (what to
// read, what counts as a bid button) lives in plugins/<name>/.

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

// runWatch implements `picu watch [--session S] [--plugin P]
// [--interval D]`: attach to the session tab and print one JSON object
// per state change, as the plugin's state.js defines it.
func runWatch(o fetchOpts, plugin string, interval time.Duration) {
	stateJS := pluginJS(plugin, "state.js")
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
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	var last string
	first := true
	for {
		raw, err := eval(stateJS)
		if err != nil {
			fmt.Fprintf(os.Stderr, "watch: read failed (%v) — retrying\n", err)
			time.Sleep(interval)
			continue
		}
		var probe map[string]any
		if jerr := json.Unmarshal([]byte(raw), &probe); jerr != nil || probe == nil {
			fmt.Fprintf(os.Stderr, "watch: parse failed — retrying\n")
			time.Sleep(interval)
			continue
		}
		if first || raw != last {
			last = raw
			first = false
			probe["ts"] = time.Now().UTC().Format(time.RFC3339)
			probe["url"] = sessionURL(eval)
			enc := json.NewEncoder(os.Stdout)
			enc.SetEscapeHTML(false)
			_ = enc.Encode(probe)
		}
		time.Sleep(interval)
	}
}

// runBid implements `picu bid --session S MAX`: one explicit bid at or
// under MAX, as the plugin's bid.js defines it. Each invocation is one
// consent.
func runBid(o fetchOpts, plugin string, max float64) {
	bidJS := pluginJS(plugin, "bid.js")
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
	raw, err := eval("(" + bidJS + ")(" + strconv.FormatFloat(max, 'f', -1, 64) + ")")
	if err != nil {
		fatal(fmt.Sprintf("bid: %v", err))
	}
	fmt.Println(raw)
}
