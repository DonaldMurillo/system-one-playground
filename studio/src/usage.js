// Published Jev 1.13 rate, verified 2026-09-19: https://docs.typesafe.ai/models
// Per-operation snapshots, not cumulative account billing.
export const usageCost = tokens => `$${((tokens || 0) * 0.042 / 1e6).toFixed(8)}`

export function usageLine(u) {
  if (!u || typeof u !== 'object') return ''
  const parts = [`requests ${u.totalAdmitted ?? 0} of ${u.totalLimit ?? 0}`]
  const buckets = u.buckets || {}
  for (const name of ['editor', 'interpretation', 'runtime']) {
    const b = buckets[name]
    if (!b) continue
    let line = `${name} ${b.requests ?? 0}/${b.limit ?? 0}`
    if (b.reportedInputTokens) line += ` · ${b.reportedInputTokens} input tokens`
    if (b.unresolved) line += ` · ${b.unresolved} unresolved`
    parts.push(line)
  }
  const tokens = Object.values(buckets).reduce((n,b) => n + (b.reportedInputTokens || 0), 0)
  const unknown = Object.values(buckets).reduce((n,b) => n + (b.unresolved || 0), 0)
  parts.push(`Jev 1.13 rate estimate ${usageCost(tokens)} USD${unknown ? ' + unknown usage' : ''}`)
  return 'usage: ' + parts.join(' · ') + ' · estimate only, $0.042/M input tokens; not an invoice'
}
