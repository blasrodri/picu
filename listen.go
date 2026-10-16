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
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// listenJS records seconds of the show's audio and resolves base64 webm.
const listenJS = `(seconds => new Promise(async (resolve, reject) => {
  // Prefer the SDK's live audio track: its MediaStreamTrack feeds our
  // own graph with no playback involved. Fall back to the site's video
  // element (video-only on some shows: expect silence then).
  let mediaStream = null, vid = null, wasMuted = true;
  let trackIds = '';
  try {
    const tracks = (window.__ARTC__ && window.__ARTC__.__TRACK_LIST__) || {};
    for (const id of Object.keys(tracks)) {
      const t = tracks[id];
      const kind = t && (t.trackMediaType || t._trackMediaType);
      trackIds += id + ':' + kind + ';';
      if (kind === 'audio' && t.getMediaStreamTrack) {
        const mst = t.getMediaStreamTrack();
        if (mst && mst.readyState === 'live') { mediaStream = new MediaStream([mst]); break; }
      }
    }
  } catch (e) {}
  // Shows swap tracks without reloading: rebuild the graph when the
  // set changes instead of recording a dead stream.
  if (window.__picuAC && window.__picuAC.trackIds !== trackIds) {
    try { window.__picuAC.ac.close(); } catch (e) {}
    window.__picuAC = null;
  }
  try {
    if (!mediaStream) {
      vid = document.querySelector('video.agora_video_player') || document.querySelector('video');
      if (!vid) { reject('no video element'); return; }
      // A muted element feeds silence to the tap by spec: unmute for
      // the clip (autoplay policy is lifted in our flags, headless has
      // no speakers), then restore. The player re-mutes aggressively,
      // so the internal slot is cleared first, later writes swallowed.
      wasMuted = vid.muted;
      vid.muted = false;
      vid.volume = 1;
      try { Object.defineProperty(vid, 'muted', {configurable: true, get: () => false, set: () => {}}); } catch (e) {}
      if (vid.paused) { try { await vid.play(); } catch (e) {} }
    }
    if (!window.__picuAC) {
      const AC = window.AudioContext || window.webkitAudioContext;
      const ac = new AC();
      const dest = ac.createMediaStreamDestination();
      if (mediaStream) {
        ac.createMediaStreamSource(mediaStream).connect(dest);
      } else {
        const src = ac.createMediaElementSource(vid);
        src.connect(dest);
        src.connect(ac.destination);
      }
      window.__picuAC = {ac, dest, via: mediaStream ? 'track' : 'element', trackIds};
    }
    const {ac, dest} = window.__picuAC;
    if (ac.state === 'suspended') ac.resume();
    const rec = new MediaRecorder(dest.stream, {mimeType: 'audio/webm;codecs=opus'});
    const chunks = [];
    rec.ondataavailable = e => { if (e.data && e.data.size > 0) chunks.push(e.data); };
    rec.onstop = () => {
      if (vid) { try { vid.muted = wasMuted; } catch (e) {} }
      const blob = new Blob(chunks, {type: 'audio/webm'});
      const fr = new FileReader();
      fr.onloadend = () => resolve({audio: fr.result, diag: {
        via: window.__picuAC.via,
        muted: vid ? vid.muted : null,
        srcObjAudio: (vid && vid.srcObject ? vid.srcObject.getAudioTracks().length : -1),
        agoraKeys: Object.keys(window).filter(k => /agora|rtc|IVS|ivs|player/i.test(k)).slice(0, 10),
        artc: (() => { try {
          const g = window.__ARTC__;
          const tracks = g.__TRACK_LIST__ || {};
          const info = [];
          for (const id of Object.keys(tracks).slice(0, 6)) {
            const t = tracks[id];
            const row = {id};
            try {
              row.type = t.trackMediaType || t._trackMediaType || '?';
              row.playing = !!t.isPlaying;
              row.muted = !!t.muted;
              row.hasPlay = typeof t.play === 'function';
              row.keys = Object.keys(Object.getPrototypeOf(t) || {}).slice(0, 12);
            } catch (e) { row.err = 'x'; }
            info.push(row);
          }
          return JSON.stringify(info).slice(0, 800);
        } catch (e) { return 'n/a'; } })(),
        acState: window.__picuAC.ac.state, bytes: blob.size}});
      fr.onerror = () => reject('encode failed');
      fr.readAsDataURL(blob);
    };
    rec.onerror = () => reject('recorder error');
    rec.start();
    setTimeout(() => rec.stop(), seconds * 1000);
  } catch (e) { reject('tap failed: ' + e); }
}))`

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

// captureClip records seconds of show audio via the in-page tap.
func captureClip(taskCtx context.Context, seconds int) ([]byte, map[string]any, error) {
	ctx, cancel := context.WithTimeout(taskCtx, time.Duration(seconds+60)*time.Second)
	defer cancel()
	res, err := chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{
		Expression:    fmt.Sprintf("(%s)(%d)", listenJS, seconds),
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

var spokenPriceRe = regexp.MustCompile(`[€$£]\s?[\d.,]+|\b\d[\d.,]*\s?(?:euros?|dollars?|usd)\b`)
var soldCallRe = regexp.MustCompile(`(?i)\bsold\b|vendu|vendido|adjudicad|\bwon\b|congrats|félicitations|enhorabuena`)

// parseShow fuses tab state with seller speech: what product, what
// price, whether the hammer fell. Deterministic rules, no model — the
// DOM owns status, speech confirms prices and winner callouts.
func parseShow(st watchState, tr transcript) map[string]any {
	var spoken []string
	for _, sgm := range tr.Segments {
		for _, m := range spokenPriceRe.FindAllString(sgm.Text, -1) {
			spoken = append(spoken, strings.TrimSpace(m))
		}
	}
	full := ""
	for _, sgm := range tr.Segments {
		full += " " + sgm.Text
	}
	soldCall := soldCallRe.MatchString(full)
	verdict := "idle"
	switch {
	case st.Status == "live" && st.Price != "":
		verdict = fmt.Sprintf("LIVE: %s at %s", strings.TrimSpace(st.Product), st.Price)
	case st.Status == "live":
		verdict = fmt.Sprintf("LIVE: %s (no price yet)", strings.TrimSpace(st.Product))
	case st.Winner != "":
		verdict = fmt.Sprintf("SOLD to %s: %s", st.Winner, strings.TrimSpace(st.Product))
	case soldCall:
		verdict = "hammer fell on air: " + strings.TrimSpace(st.Product)
	case strings.TrimSpace(st.Product) != "":
		verdict = "between lots: " + strings.TrimSpace(st.Product)
	}
	return map[string]any{
		"verdict": verdict, "spokenPrices": spoken, "soldCall": soldCall,
		"product": st.Product, "price": st.Price, "status": st.Status, "winner": st.Winner,
	}
}

// runListen implements `picu listen --session S [seconds] [--follow]`:
// one clip by default, back-to-back clips with a warm model on follow.
func runListen(o fetchOpts, seconds int, follow bool) {
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
		if err := listenOnce(o, s, stt, seconds); err != nil {
			fmt.Fprintf(os.Stderr, "listen: %v — retrying\n", err)
		}
		if !follow {
			return
		}
	}
}

func listenOnce(o fetchOpts, s *browserSession, stt *sttServer, seconds int) error {
	taskCtx, eval, cancelTask, cancelAlloc, cancelParent, err := sessionCtx(s, o)
	if err != nil {
		return err
	}
	defer cancelParent()
	defer cancelAlloc()
	defer cancelTask()
	raw, diag, err := captureClip(taskCtx, seconds)
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
	var st watchState
	if sraw, serr := eval(whatnotStateJS); serr == nil {
		_ = json.Unmarshal([]byte(sraw), &st)
	}
	segs := tr.Segments
	if segs == nil {
		segs = []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		}{}
	}
	parsed := parseShow(st, tr)
	parsed["ts"] = time.Now().UTC().Format(time.RFC3339)
	parsed["session"] = s.Name
	parsed["clipSeconds"] = seconds
	parsed["language"] = tr.Language
	parsed["segments"] = segs
	parsed["diag"] = diag
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(parsed)
}
