package main

// listen captures seller audio from a live-show tab and transcribes it
// locally. The player is Agora WebRTC (no playlist to download), so the
// tap happens in-page: the video element feeds an AudioContext whose
// MediaStreamDestination is recorded, chunked, and returned as base64.
// Transcription runs in the local faster-whisper venv; nothing uploads.
//
// One invocation = one clip (--seconds, default 15). Loop it externally
// for continuous coverage, same as watch.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// sttModel picks speed vs accuracy: tiny halves latency, base hears
// better. Override with PICU_STT_MODEL.
func sttModel() string {
	if m := os.Getenv("PICU_STT_MODEL"); m != "" {
		return m
	}
	return "base"
}

// sttHelper is a warm transcription server: it loads the model once,
// then reads one audio path per stdin line and prints one JSON object
// per line. No per-clip model load, which is what makes follow mode
// real-time instead of slideshow.
const sttHelper = `
import json, sys
import av
_orig_open = av.open
def _open(file, mode="r", *a, **k):
    k.pop("metadata_errors", None)  # av>=19 dropped it; faster-whisper still sends it
    return _orig_open(file, mode=mode, *a, **k)
av.open = _open
from faster_whisper import WhisperModel
import os
model = WhisperModel(os.environ.get("PICU_STT_MODEL", "base"), device="auto", compute_type="auto")
for line in sys.stdin:
    path = line.strip()
    if not path:
        continue
    try:
        segs, info = model.transcribe(path, beam_size=1, vad_filter=True)
        out = [{"start": round(s.start, 1), "end": round(s.end, 1), "text": s.text.strip()} for s in segs]
        print(json.dumps({"language": info.language, "segments": out}), flush=True)
    except Exception as e:
        print(json.dumps({"error": str(e)[:200], "segments": []}), flush=True)
`

func sttVenv() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	py := filepath.Join(home, ".local", "share", "picu", "stt", "bin", "python")
	if _, err := os.Stat(py); err != nil {
		return ""
	}
	return py
}

// sttServer keeps one model resident: write an audio path per stdin
// line, read one transcript object per stdout line.
type sttServer struct {
	cmd *exec.Cmd
	in  io.Writer
	rd  *bufio.Reader
}

func startSTT() (*sttServer, error) {
	py := sttVenv()
	if py == "" {
		return nil, fmt.Errorf("no STT engine — install faster-whisper into ~/.local/share/picu/stt")
	}
	helper, err := os.CreateTemp("", "picu-stt-*.py")
	if err != nil {
		return nil, err
	}
	if _, err := helper.WriteString(sttHelper); err != nil {
		helper.Close()
		os.Remove(helper.Name())
		return nil, err
	}
	helper.Close()
	cmd := exec.Command(py, helper.Name())
	cmd.Env = append(os.Environ(), "HF_HUB_OFFLINE=0", "PICU_STT_MODEL="+sttModel())
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &sttServer{cmd: cmd, in: in, rd: bufio.NewReader(out)}, nil
}

func (s *sttServer) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

type transcript struct {
	Language string `json:"language"`
	Error    string `json:"error"`
	Segments []struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
	} `json:"segments"`
}

func (s *sttServer) transcribe(path string) (transcript, error) {
	var tr transcript
	if _, err := fmt.Fprintln(s.in, path); err != nil {
		return tr, err
	}
	line, err := s.rd.ReadBytes('\n')
	if err != nil {
		return tr, err
	}
	if err := json.Unmarshal(line, &tr); err != nil {
		return tr, err
	}
	if tr.Error != "" {
		return tr, fmt.Errorf("%s", tr.Error)
	}
	return tr, nil
}

// captureClip records seconds of tab audio through the plugin's tap:
// tap.js called as (tap)(seconds), resolving {audio, diag}.
func captureClip(taskCtx context.Context, tapJS string, seconds int) ([]byte, map[string]any, error) {
	ctx, cancel := context.WithTimeout(taskCtx, time.Duration(seconds+60)*time.Second)
	defer cancel()
	res, err := chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{
		Expression:    fmt.Sprintf("(%s)(%d)", tapJS, seconds),
		AwaitPromise:  boolPtr(true),
		ReturnByValue: boolPtr(true),
	})
	if err != nil {
		return nil, nil, err
	}
	if res.ExceptionDetails != nil {
		return nil, nil, fmt.Errorf("%s", res.ExceptionDetails.Text)
	}
	var got struct {
		Audio string         `json:"audio"`
		Diag  map[string]any `json:"diag"`
	}
	if err := json.Unmarshal([]byte(res.Result.Value), &got); err != nil {
		return nil, nil, fmt.Errorf("decode: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(got.Audio[strings.Index(got.Audio, ",")+1:])
	if err != nil || len(raw) == 0 {
		return nil, got.Diag, fmt.Errorf("empty clip")
	}
	return raw, got.Diag, nil
}

// pluginTap resolves the tap: the plugin's tap.js, default tap.js when
// the plugin ships none.
func pluginTap(plugin string) string {
	if pluginHas(plugin, "tap.js") {
		return pluginJS(plugin, "tap.js")
	}
	return pluginJS("default", "tap.js")
}

// runListen implements `picu listen --session S [seconds] [--follow
// --plugin P]`: one clip by default, back-to-back clips with a warm
// model on follow. Audio capture (tap.js), tab state (state.js), and
// verdict rules (parse.js) all come from the plugin; core only
// interprets, screenshots nothing, transcribes locally.
func runListen(o fetchOpts, plugin string, seconds int, follow bool) {
	if seconds < 3 {
		seconds = 3
	}
	if seconds > 120 {
		seconds = 120
	}
	name := *sessionFlg
	s, err := loadSession(name)
	if err != nil {
		fatal(err.Error())
	}
	fmt.Fprintln(os.Stderr, "listen: warming up transcription (loads once)")
	stt, err := startSTT()
	if err != nil {
		fatal(err.Error())
	}
	defer stt.stop()
	for {
		if err := listenOnce(o, s, stt, plugin, seconds); err != nil {
			fmt.Fprintf(os.Stderr, "listen: %v — retrying\n", err)
		}
		if !follow {
			return
		}
	}
}

func listenOnce(o fetchOpts, s *browserSession, stt *sttServer, plugin string, seconds int) error {
	taskCtx, eval, cancelTask, cancelAlloc, cancelParent, err := sessionCtx(s, o)
	if err != nil {
		return err
	}
	defer cancelParent()
	defer cancelAlloc()
	defer cancelTask()
	pruneVideos(eval)
	raw, diag, err := captureClip(taskCtx, pluginTap(plugin), seconds)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "picu-clip-*.webm")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	tr, err := stt.transcribe(tmp.Name())
	if err != nil {
		return fmt.Errorf("transcribe: %v", err)
	}
	var state map[string]any
	if sraw, serr := eval(pluginJS(plugin, "state.js")); serr == nil {
		_ = json.Unmarshal([]byte(sraw), &state)
	}
	if state == nil {
		state = map[string]any{}
	}
	segs := tr.Segments
	if segs == nil {
		segs = []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		}{}
	}
	// Verdict rules are the plugin's: parse.js fuses state with speech.
	doc := map[string]any{
		"ts": time.Now().UTC().Format(time.RFC3339), "session": s.Name,
		"clipSeconds": seconds, "language": tr.Language,
		"segments": segs, "diag": diag, "state": state,
	}
	if pluginHas(plugin, "parse.js") {
		trJSON, _ := json.Marshal(tr)
		stateJSON, _ := json.Marshal(state)
		prow, perr := eval("(" + pluginJS(plugin, "parse.js") + ")(" + string(stateJSON) + "," + string(trJSON) + ")")
		if perr == nil {
			var verdict map[string]any
			if json.Unmarshal([]byte(prow), &verdict) == nil {
				for k, v := range verdict {
					doc[k] = v
				}
			}
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(doc)
}
