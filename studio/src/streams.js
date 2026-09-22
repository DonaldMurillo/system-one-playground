export function streamCanStop(stream) {
  return stream && ['open', 'reading'].includes(stream.state)
}

export function streamElapsed(stream, now = Date.now()) {
  const start = Date.parse(stream?.startedAt || '')
  const end = stream?.endedAt ? Date.parse(stream.endedAt) : now
  if (!Number.isFinite(start) || !Number.isFinite(end)) return '—'
  const milliseconds = Math.max(0, end - start)
  return milliseconds < 1000 ? `${milliseconds} ms` : `${(milliseconds / 1000).toFixed(1)} s`
}

export function streamSummary(stream) {
  const count = Number(stream?.itemsReceived || 0)
  const buffered = Number(stream?.itemsBuffered || 0)
  const credit = Number(stream?.creditAvailable || 0)
  return `${count} item${count === 1 ? '' : 's'} received · ${buffered} buffered · ${credit} credit`
}

// Filesystem traversals and watchers report their scope alongside the shared
// stream metrics: a bounded walk shows its entry limit, a watcher shows the
// watched root and its active state. Both snapshot fields are optional, so
// ordinary producers render unchanged.
export function streamScope(stream) {
  if (!stream) return ''
  const parts = []
  if (stream.watching) parts.push('watching')
  if (stream.root) parts.push(String(stream.root))
  if (Number(stream.bound) > 0) parts.push(`bound ${stream.bound}`)
  return parts.join(' · ')
}
