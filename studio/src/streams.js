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

export function streamTimeDetail(stream) {
  if (!stream) return ''
  const parts = []
  if (stream.clockKind) {
    parts.push(stream.clockKind === 'virtual'
      ? `virtual clock${stream.virtualTime ? ` at ${stream.virtualTime}` : ''}`
      : 'host clock')
  }
  if (stream.interval) parts.push(`every ${stream.interval}`)
  if (stream.schedule) parts.push(String(stream.schedule))
  if (stream.timeZone) parts.push(String(stream.timeZone))
  if (stream.nextScheduledAt) parts.push(`next ${stream.nextScheduledAt}`)
  const counts = [['missed', stream.missedTicks], ['combined', stream.combinedTicks], ['skipped', stream.skippedTicks], ['caught up', stream.caughtUpTicks]]
    .filter(([, value]) => Number(value) > 0)
    .map(([name, value]) => `${value} ${name}`)
  if (counts.length) parts.push(counts.join(' · '))
  if (stream.deadlineRemaining) parts.push(`${stream.deadlineRemaining} of deadline left`)
  if (stream.checkpointIdentity) parts.push(`checkpoint ${stream.checkpointIdentity}`)
  return parts.join(' · ')
}

export function streamSummary(stream) {
  const count = Number(stream?.itemsReceived || 0)
  const buffered = Number(stream?.itemsBuffered || 0)
  const credit = Number(stream?.creditAvailable || 0)
  return `${count} item${count === 1 ? '' : 's'} received · ${buffered} buffered · ${credit} credit`
}
