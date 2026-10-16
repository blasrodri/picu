// Whatnot verdict rules: fuse tab state with seller speech into
// product / price / hammer calls. Deterministic, no model — the DOM
// owns status, speech confirms prices, hammer cues, and whether the
// seller's words match the listed lot at all.
// Called as (parse)(state, transcript) where transcript is
// {language, segments: [{start, end, text}]}.
((state, tr) => {
  const priceRe = /[€$£]\s?\d+(?:[.,]\d+)?|\b\d+(?:[.,]\d+)?\s?(?:euros?|dollars?|usd)\b/g;
  const cueDefs = [
    ['sold', /\bsold\b|vendu|vendido|adjudicad|\bwon\b|verkauft/i],
    ['going-once', /going once|une fois|zum Ersten|a la una/i],
    ['going-twice', /going twice|deux fois|zum Zweiten|a las dos/i],
    ['congrats', /congrats|félicitations|enhorabuena|glückwunsch/i],
    ['next-lot', /next (lot|item|product)|lot suivant|nächste| siguiente/i],
    ['opening-bid', /start(?:ing)? (?:at|from|bid)|on commence|wir starten|empezamos/i],
  ];
  const stop = new Set(['the', 'avec', 'pas', 'und', 'der', 'die', 'das',
    'una', 'los', 'las', 'open-box', 'openbox', 'bids', 'qty',
    'shipping', 'taxes', 'extra', 'available']);
  const words = String(state.product || '').toLowerCase().split(/\s+/)
    .map(w => w.replace(/^[.,!?:;()#]+|[.,!?:;()#]+$/g, ''))
    .filter(w => w.length >= 4 && !stop.has(w));
  const spoken = [];
  let full = '';
  for (const s of (tr.segments || [])) {
    const m = String(s.text || '').match(priceRe);
    if (m) for (const p of m) spoken.push(p.trim());
    full += ' ' + (s.text || '');
  }
  const cues = [];
  for (const [name, re] of cueDefs) { if (re.test(full)) cues.push(name); }
  const low = full.toLowerCase();
  const pitched = words.length > 0 && words.some(w => low.includes(w));
  const product = String(state.product || '').trim();
  let verdict = 'idle';
  if (state.status === 'live' && state.price && pitched) {
    verdict = `LIVE pitched: ${product} at ${state.price}`;
  } else if (state.status === 'live' && state.price) {
    verdict = `LIVE (seller not pitching it): ${product} at ${state.price}`;
  } else if (state.status === 'live') {
    verdict = `LIVE: ${product} (no price yet)`;
  } else if (state.winner) {
    verdict = `SOLD to ${state.winner}: ${product}`;
  } else if (cues.length > 0 && product) {
    verdict = `hammer (${cues.join(',')}): ${product}`;
  } else if (product) {
    verdict = `between lots: ${product}`;
  }
  return JSON.stringify({verdict, spokenPrices: spoken, hammerCues: cues,
    pitchMatch: pitched, product: state.product, price: state.price,
    status: state.status, winner: state.winner});
})
