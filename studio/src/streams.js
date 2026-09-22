export function streamCanStop(stream) {
  return stream && ['open', 'reading'].includes(stream.state)
}

const terminalStreamStates = ['completed', 'stopped', 'cancelled', 'failed', 'closed']

function streamTerminal(stream) {
  return Boolean(stream) && terminalStreamStates.includes(stream.state)
}

// streamProducer splits the derived-stream producer "parentProducer/op" into
// its source and the derived operation applied to it.
export function streamProducer(stream) {
  const producer = stream?.producer || ''
  const slash = producer.indexOf('/')
  return slash > 0 ? `${producer.slice(0, slash)} → ${producer.slice(slash + 1)}` : producer
}

export function streamSubtitle(stream) {
  const parts = []
  const producer = streamProducer(stream)
  if (producer) parts.push(producer)
  if (stream?.itemType) parts.push(`stream of ${stream.itemType}`)
  if (stream?.binding) parts.push(`binding: ${stream.binding}`)
  if (stream?.line) parts.push(`line ${stream.line}`)
  return parts.join(' · ')
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
  const parts = [`${count} item${count === 1 ? '' : 's'} received`, `${buffered} buffered`, `${credit} credit`]
  if (stream?.policy) {
    const keys = Number(stream.policy.keys || 0)
    const maxKeys = Number(stream.policy.max_keys || 0)
    parts.push(`${keys} keys (${maxKeys} max)`)
    parts.push(`${Number(stream.policy.pending || 0)} pending`)
    parts.push(`${Number(stream.policy.timers || 0)} timers`)
  }
  if (streamTerminal(stream) && stream.reason) parts.push(`reason: ${stream.reason}`)
  return parts.join(' · ')
}
