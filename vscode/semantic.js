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

module.exports = { decisionLensTitle }
