(() => {
const txt = el => (el && el.innerText || '').trim().replace(/\s+/g, ' ');
const first = els => { for (const el of els) { const s = txt(el); if (s) return s; } return ''; };
const sellerLink = document.querySelector('section header div a');
const seller = sellerLink ? txt(sellerLink) : '';
let viewers = '';
const hdrBtns = document.querySelectorAll('section header section button');
if (hdrBtns.length > 0) viewers = txt(hdrBtns[0]);
const footBtn = document.querySelector('section footer section button');
const foot = txt(footBtn);
let bidPrice = '', canBid = false;
for (const b of document.querySelectorAll('button')) {
  const m = txt(b).match(/^Bid:\s*(.+)$/);
  if (m) { bidPrice = m[1]; canBid = true; break; }
}
let winner = '';
const sold = foot.match(/[Ss]old\s+(?:to\s+)?(\S.{0,40}?)(?:\s{2,}|$)/);
if (sold) winner = sold[1].trim();
const cleanChat = t => {
  const lines = (t || '').split('\n').map(s => s.trim())
    .filter(s => s && s.length > 1 && !/^(Follow|Say something\.\.\.)$/i.test(s));
  const out = [];
  for (const l of lines) {
    if (out.length === 0 || out[out.length - 1] !== l) out.push(l);
  }
  return out.slice(-8);
};
const vis = el => { try { return el.getClientRects().length > 0; } catch (e) { return false; } };
// Chat rows carry data-testid="chat-message" (avatar, username, text
// in innerText). Hidden mobile duplicates are skipped by visibility.
let chat = [];
{
  const rows = document.querySelectorAll('[data-testid="chat-message"]');
  const seen = [];
  for (let i = 0; i < rows.length; i++) {
    if (!vis(rows[i])) continue;
    const t = (rows[i].innerText || '').trim().replace(/\s+/g, ' ');
    if (t) seen.push(t.slice(0, 220));
  }
  chat = cleanChat(seen.slice(-6).join('\n'));
}
if (!winner) {
  for (let i = chat.length - 1; i >= 0; i--) {
    const m = chat[i].match(/^(.{2,30}?)\s+won\b/i) || chat[i].match(/congrats\s+(\S.{0,30}?)\s*(!|$)/i);
    if (m) { winner = m[1].trim(); break; }
  }
}
let status = 'off';
if (/auction hasn't started/i.test(foot)) status = 'pre';
else if (canBid) status = 'live';
else if (winner) status = 'sold';
let streamUrl = '';
try {
  const entries = performance.getEntriesByType('resource') || [];
  for (let i = entries.length - 1; i >= 0; i--) {
    const u = entries[i].name || '';
    if (/\.(m3u8|mpd)(\?|$)/.test(u)) { streamUrl = u.slice(0, 500); break; }
  }
  if (!streamUrl) {
    // No playlist in the buffer (MSE players, eviction, or WebRTC):
    // report how many subresource fetches look like media segments.
    let seg = 0;
    for (let i = 0; i < entries.length; i++) {
      const u = entries[i].name || '';
      if (/\.(ts|m4s|mp4)(\?|$)/.test(u)) seg++;
    }
    if (seg > 0) streamUrl = 'segments:' + seg;
  }
  if (!streamUrl) {
    const vid = document.querySelector('video');
    const vsrc = vid ? (vid.currentSrc || vid.src || '') : '';
    if (vsrc) streamUrl = 'video:' + vsrc.slice(0, 300);
  }
} catch (e) {}
return JSON.stringify({seller, viewers, product: foot.slice(0, 300),
  price: bidPrice, canBid, status, winner, chat, streamUrl});
})()
