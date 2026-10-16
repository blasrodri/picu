// Default audio tap: records the page's own media elements. No SDK
// knowledge. Resolves {audio: dataURL, diag}. Called as (tap)(seconds).
((seconds) => new Promise(async (resolve, reject) => {
  const el = document.querySelector('video') || document.querySelector('audio');
  if (!el) { reject('no media element'); return; }
  const wasMuted = el.muted;
  try {
    el.muted = false;
    el.volume = 1;
    if (el.paused) { try { await el.play(); } catch (e) {} }
    const AC = window.AudioContext || window.webkitAudioContext;
    const ac = new AC();
    const dest = ac.createMediaStreamDestination();
    const src = ac.createMediaElementSource(el);
    src.connect(dest);
    src.connect(ac.destination);
    if (ac.state === 'suspended') ac.resume();
    const rec = new MediaRecorder(dest.stream, {mimeType: 'audio/webm;codecs=opus'});
    const chunks = [];
    rec.ondataavailable = e => { if (e.data && e.data.size > 0) chunks.push(e.data); };
    rec.onstop = () => {
      try { el.muted = wasMuted; } catch (e) {}
      const blob = new Blob(chunks, {type: 'audio/webm'});
      const fr = new FileReader();
      fr.onloadend = () => resolve({audio: fr.result, diag: {via: 'element', bytes: blob.size}});
      fr.onerror = () => reject('encode failed');
      fr.readAsDataURL(blob);
    };
    rec.onerror = () => reject('recorder error');
    rec.start();
    setTimeout(() => rec.stop(), seconds * 1000);
  } catch (e) { reject('tap failed: ' + e); }
}))
