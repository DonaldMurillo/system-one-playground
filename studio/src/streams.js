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
