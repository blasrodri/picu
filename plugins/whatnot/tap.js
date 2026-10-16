// Whatnot audio tap: prefers the Agora SDK's live audio track (its
// MediaStreamTrack feeds our graph with no playback involved), falls
// back to the page's video element. Resolves {audio: dataURL, diag}.
// Called as (tap)(seconds).
((seconds) => new Promise(async (resolve, reject) => {
  let mediaStream = null, vid = null, wasMuted = true, trackIds = '';
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
    const ac = window.__picuAC.ac, dest = window.__picuAC.dest;
    if (ac.state === 'suspended') ac.resume();
    const rec = new MediaRecorder(dest.stream, {mimeType: 'audio/webm;codecs=opus'});
    const chunks = [];
    rec.ondataavailable = e => { if (e.data && e.data.size > 0) chunks.push(e.data); };
    rec.onstop = () => {
      if (vid) { try { vid.muted = wasMuted; } catch (e) {} }
      const blob = new Blob(chunks, {type: 'audio/webm'});
      const fr = new FileReader();
      fr.onloadend = () => resolve({audio: fr.result, diag: {
        via: window.__picuAC.via, bytes: blob.size,
        muted: vid ? vid.muted : null}});
      fr.onerror = () => reject('encode failed');
      fr.readAsDataURL(blob);
    };
    rec.onerror = () => reject('recorder error');
    rec.start();
    setTimeout(() => rec.stop(), seconds * 1000);
  } catch (e) { reject('tap failed: ' + e); }
}))
