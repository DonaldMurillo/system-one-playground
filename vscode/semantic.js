'use strict'

function decisionLensTitle(decision) {
  const confidence = `${Math.round((decision.confidence || 0) * 100)}% confidence`
  if (decision.method === 'memoized') return `Jev memoized · ${confidence} · 0 new requests · $0.00000000`
  if (decision.method === 'jev') {
    const usage = decision.usage_known
      ? `${decision.input_tokens || 0} tokens · ~$${(((decision.input_tokens || 0) * 0.042) / 1e6).toFixed(8)}${decision.usage_shared ? ` shared across ${decision.batch_size} lines` : ''}`
      : 'usage unavailable'
    return `Jev · ${confidence} · ${usage}`
  }
  return `Deterministic · ${confidence} · no Jev cost`
}

function analyzedLineMatches(current, analyzed) {
  let quoted = false
  let escaped = false
  let semantic = ''
  for (const char of current.trim()) {
    if (!quoted && char === '#') break
    semantic += char
    if (escaped) { escaped = false; continue }
    if (quoted && char === '\\') { escaped = true; continue }
    if (char === '"') quoted = !quoted
  }
  return semantic.trim() === analyzed.trim()
}

module.exports = { decisionLensTitle, analyzedLineMatches }
