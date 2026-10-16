(max => {
for (const b of document.querySelectorAll('button')) {
  const m = (b.innerText || '').trim().replace(/\s+/g, ' ').match(/^Bid:\s*€?\s*([\d.,]+)/);
  if (!m) continue;
  const price = parseFloat(m[1].replace(/,/g, ''));
  if (price <= max) { b.click(); return JSON.stringify({clicked: true, price: m[1]}); }
  return JSON.stringify({clicked: false, price: m[1], reason: 'over max'});
}
return JSON.stringify({clicked: false, reason: 'no live bid button'});
})
