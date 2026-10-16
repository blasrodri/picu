package main

// see asks the local vision model about a session tab's key frame.
// One screenshot per invocation (or per --interval tick), never a frame
// pump: the DOM watch feed owns continuity, Gemma owns the picture.
// Output stays text JSON: the frame never leaves the machine — model,
// browser, and stream all run locally.
//
// Model endpoint and name ride env so agents can swap them without new
// flags: PICU_OLLAMA_URL (default http://localhost:11434) and
// PICU_VISION_MODEL (default gemma4:12b).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

func ollamaURL() string {
	if u := os.Getenv("PICU_OLLAMA_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:11434"
}

func visionModel() string {
	if m := os.Getenv("PICU_VISION_MODEL"); m != "" {
		return m
	}
	return "gemma3:4b"
}

func i64Ptr(v int64) *int64 { return &v }

const defaultSeeQuestion = "Is a product being auctioned right now? Answer exactly: yes/no, then the product name, then the visible price (or 'no price shown'), then one line on what the seller is presenting."

// askFrame posts one JPEG frame plus grounding text to the local model
// and returns its answer.
func askFrame(jpg []byte, grounding, question string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":  visionModel(),
		"prompt": grounding + "\n\n" + question,
		"images": []string{base64.StdEncoding.EncodeToString(jpg)},
		"stream": false,
	})
	req, err := http.NewRequest("POST", ollamaURL()+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 180 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("vision model unreachable at %s (%v)", ollamaURL(), err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode != 200 {
		n := len(raw)
		if n > 200 {
			n = 200
		}
		return "", fmt.Errorf("vision model http %d: %s", res.StatusCode, string(raw[:n]))
	}
	var out struct {
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("vision model: %s", out.Error)
	}
	return strings.TrimSpace(out.Response), nil
}

// runSee implements `picu see --session S [--interval D] [--plugin P]
// [question]`: one key frame per tick, each answered by the local
// vision model. Tab state for grounding comes from the plugin.
func runSee(o fetchOpts, plugin string, interval time.Duration, loop bool, question string) {
	name := *sessionFlg
	s, err := loadSession(name)
	if err != nil {
		fatal(err.Error())
	}
	taskCtx, eval, cancelTask, cancelAlloc, cancelParent, err := sessionCtx(s, o)
	if err != nil {
		fatal(err.Error())
	}
	defer cancelParent()
	defer cancelAlloc()
	defer cancelTask()
	if question == "" {
		question = defaultSeeQuestion
	}
	for {
		if err := seeOnce(taskCtx, eval, s, plugin, question); err != nil {
			fmt.Fprintf(os.Stderr, "see: %v — retrying\n", err)
		}
		if !loop {
			return
		}
		if interval < 10*time.Second {
			interval = 10 * time.Second
		}
		time.Sleep(interval)
	}
}

func seeOnce(taskCtx context.Context, eval func(string) (string, error), s *browserSession, plugin string, question string) error {
	raw, err := eval(pluginJS(plugin, "state.js"))
	if err != nil {
		return err
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return err
	}
	q := i64Ptr(60)
	shot, err := chromedp.Call(taskCtx, page.CaptureScreenshot, page.CaptureScreenshotParams{
		Format: page.CaptureScreenshotFormatJpeg, Quality: q,
	})
	if err != nil {
		return fmt.Errorf("screenshot: %v", err)
	}
	if len(shot.Data) == 0 {
		return fmt.Errorf("screenshot came back empty")
	}
	grounding := fmt.Sprintf("Live tab state: %s", raw)
	answer, err := askFrame(shot.Data, grounding, question)
	if err != nil {
		return err
	}
	doc := map[string]any{
		"ts": time.Now().UTC().Format(time.RFC3339), "session": s.Name,
		"state": st, "frameBytes": len(shot.Data), "answer": answer,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(doc)
}

// flagPassed reports whether the flag was set on this command line, so
// `see` can default to one shot and loop only on explicit --interval.
func flagPassed(name string) bool {
	for _, a := range os.Args {
		if a == "--"+name || strings.HasPrefix(a, "--"+name+"=") || a == "-"+name {
			return true
		}
	}
	return false
}
