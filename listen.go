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
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// sttHelper transcribes one audio file to JSON segments on stdout.
const sttHelper = `
import json, sys
import av
_orig_open = av.open
def _open(file, mode="r", *a, **k):
    k.pop("metadata_errors", None)  # av>=19 dropped it; faster-whisper still sends it
    return _orig_open(file, mode=mode, *a, **k)
av.open = _open
from faster_whisper import WhisperModel
model = WhisperModel("base", device="auto", compute_type="auto")
segs, info = model.transcribe(sys.argv[1], beam_size=1, vad_filter=True)
out = [{"start": round(s.start, 1), "end": round(s.end, 1), "text": s.text.strip()} for s in segs]
print(json.dumps({"language": info.language, "segments": out}))
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

// runListen implements `picu listen --session S [--seconds N]`.
func runListen(o fetchOpts, seconds int) {
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
	taskCtx, _, cancelTask, cancelAlloc, cancelParent, err := sessionCtx(s, o)
	if err != nil {
		fatal(err.Error())
	}
	defer cancelParent()
	defer cancelAlloc()
	defer cancelTask()
	ctx, cancel := context.WithTimeout(taskCtx, time.Duration(seconds+60)*time.Second)
	defer cancel()
	res, err := chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{
		Expression:    fmt.Sprintf("(%s)(%d)", listenJS, seconds),
		AwaitPromise:  boolPtr(true),
		ReturnByValue: boolPtr(true),
	})
	if err != nil {
		fatal(fmt.Sprintf("listen: %v", err))
	}
	if res.ExceptionDetails != nil {
		fatal(fmt.Sprintf("listen: %s", res.ExceptionDetails.Text))
	}
	var got struct {
		Audio string         `json:"audio"`
		Diag  map[string]any `json:"diag"`
	}
	if err := json.Unmarshal([]byte(res.Result.Value), &got); err != nil {
		fatal(fmt.Sprintf("listen: decode: %v", err))
	}
	raw, err := base64.StdEncoding.DecodeString(got.Audio[strings.Index(got.Audio, ",")+1:])
	if err != nil || len(raw) == 0 {
		fatal("listen: empty clip (seller muted? autoplay blocked?)")
	}
	py := sttVenv()
	if py == "" {
		fatal("listen: no STT engine — install faster-whisper into ~/.local/share/picu/stt")
	}
	tmp, err := os.CreateTemp("", "picu-clip-*.webm")
	if err != nil {
		fatal(err.Error())
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		fatal(err.Error())
	}
	tmp.Close()
	helper, err := os.CreateTemp("", "picu-stt-*.py")
	if err != nil {
		fatal(err.Error())
	}
	defer os.Remove(helper.Name())
	if _, err := helper.WriteString(sttHelper); err != nil {
		helper.Close()
		fatal(err.Error())
	}
	helper.Close()
	cmd := exec.Command(py, helper.Name(), tmp.Name())
	cmd.Env = append(os.Environ(), "HF_HUB_OFFLINE=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		fatal(fmt.Sprintf("listen: transcribe: %v\n%s", err, string(out)))
	}
	var tr struct {
		Language string `json:"language"`
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(out, &tr); err != nil {
		fatal(fmt.Sprintf("listen: parse transcript: %v", err))
	}
	doc := map[string]any{
		"ts": time.Now().UTC().Format(time.RFC3339), "session": s.Name,
		"clipSeconds": seconds, "clipBytes": len(raw), "diag": got.Diag,
		"language": tr.Language, "segments": tr.Segments,
	}
	if doc["segments"] == nil {
		doc["segments"] = []any{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(doc)
}
