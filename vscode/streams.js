'use strict'

class StreamStore {
  constructor() { this.sessions = new Map() }
  begin(session, details = {}) {
    this.sessions.set(session, { session, ...details, status:'running', streams:new Map() })
    const ended = [...this.sessions.values()].filter(item => item.status === 'ended')
    for (const stale of ended.slice(0, Math.max(0, ended.length - 10))) this.sessions.delete(stale.session)
  }
  apply(session, event) {
    let current = this.sessions.get(session)
    if (!current) { this.begin(session); current = this.sessions.get(session) }
    const previous = current.streams.get(event.id)
    if (previous?.updatedAt && event.updatedAt && Date.parse(event.updatedAt) < Date.parse(previous.updatedAt)) return
    current.streams.set(event.id, {...(previous || {}), ...event})
  }
  end(session, exitCode) {
    const current = this.sessions.get(session); if (!current) return
    current.status = 'ended'; current.exitCode = exitCode
    for (const [id, stream] of current.streams) {
      if (['open','reading','stopping'].includes(stream.state)) current.streams.set(id, {...stream, state:'unknown'})
    }
  }
  replace(session, streams) {
    const current = this.sessions.get(session); if (!current) return
    for (const stream of streams || []) this.apply(session, stream)
  }
  list() { return [...this.sessions.values()].flatMap(session => [...session.streams.values()].map(stream => ({...stream, session:session.session, sessionLabel:session.label, workspaceRoot:session.root, sessionStatus:session.status}))) }
  sessionsList() { return [...this.sessions.values()] }
}

function canStopStream(stream) { return ['open','reading'].includes(stream?.state) }

function timeSummary(stream) {
  if (!stream) return ''
  const parts = []
  if (stream.clockKind) parts.push(stream.clockKind === 'virtual'
    ? `virtual clock${stream.virtualTime ? ` at ${stream.virtualTime}` : ''}` : 'host clock')
  if (stream.interval) parts.push(`every ${typeof stream.interval === 'number' ? `${stream.interval / 1e9} seconds` : stream.interval}`)
  if (stream.schedule) parts.push(String(stream.schedule))
  if (stream.timeZone) parts.push(String(stream.timeZone))
  if (stream.nextScheduledAt) parts.push(`next ${stream.nextScheduledAt}`)
  if (stream.timerPolicy) parts.push(String(stream.timerPolicy))
  const counts = [['missed', stream.missedTicks], ['combined', stream.combinedTicks], ['skipped', stream.skippedTicks], ['caught up', stream.caughtUpTicks]]
    .filter(([, value]) => Number(value) > 0).map(([name, value]) => `${value} ${name}`)
  if (counts.length) parts.push(counts.join(' · '))
  if (stream.deadlineRemaining) parts.push(`${stream.deadlineRemaining} of deadline left`)
  return parts.join(' · ')
}

function traceLine(session, event) {
  const at = event.updatedAt ? new Date(event.updatedAt).toISOString().slice(11, 23) : '--:--:--.---'
  const count = Number(event.itemsReceived || 0)
  const origin = event.line > 0 ? ` line=${event.line}` : ''
  const reason = ['completed', 'failed', 'stopped'].includes(event.state) && event.reason ? ` reason=${event.reason}` : ''
  const policy = event.policy ? ` keys=${Number(event.policy.keys || 0)}/${Number(event.policy.max_keys || 0)} pending=${Number(event.policy.pending || 0)} timers=${Number(event.policy.timers || 0)}` : ''
  const timing = timeSummary(event)
  return `${at} ${session} ${String(event.event || 'snapshot').padEnd(9)} ${event.id} ${event.binding || '-'}${event.itemType ? ` (${event.itemType})` : ''} state=${event.state} received=${count} buffered=${Number(event.itemsBuffered || 0)} credit=${Number(event.creditAvailable || 0)} producer=${event.producer || '-'}${policy}${timing ? ` ${timing}` : ''}${origin}${reason}`
}

module.exports = { StreamStore, canStopStream, timeSummary, traceLine }
