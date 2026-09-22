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
  list() { return [...this.sessions.values()].flatMap(session => [...session.streams.values()].map(stream => ({...stream, session:session.session, sessionLabel:session.label, root:session.root, sessionStatus:session.status}))) }
  sessionsList() { return [...this.sessions.values()] }
}

function canStopStream(stream) { return ['open','reading'].includes(stream?.state) }

function traceLine(session, event) {
  const at = event.updatedAt ? new Date(event.updatedAt).toISOString().slice(11, 23) : '--:--:--.---'
  const count = Number(event.itemsReceived || 0)
  return `${at} ${session} ${String(event.event || 'snapshot').padEnd(9)} ${event.id} ${event.binding || '-'} state=${event.state} received=${count} buffered=${Number(event.itemsBuffered || 0)} credit=${Number(event.creditAvailable || 0)} producer=${event.producer || '-'}`
}

// Filesystem traversals and watchers report their scope alongside the shared
// stream metrics: a bounded walk shows its entry limit, a watcher shows the
// watched root and its active state. Both snapshot fields are optional, so
// ordinary producers render unchanged.
function streamScope(stream) {
  if (!stream) return ''
  const parts = []
  if (stream.watching) parts.push('watching')
  if (stream.root) parts.push(String(stream.root))
  if (Number(stream.bound) > 0) parts.push(`bound ${stream.bound}`)
  return parts.join(' · ')
}

module.exports = { StreamStore, canStopStream, traceLine, streamScope }
