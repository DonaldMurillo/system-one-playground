'use strict'

const net = require('node:net')
const crypto = require('node:crypto')

class StreamControlServer {
  constructor(handlers = {}) {
    this.handlers = handlers
    this.token = crypto.randomBytes(32).toString('hex')
    this.clients = new Map()
    this.pending = new Map()
    this.sequence = 0
    this.server = net.createServer(socket => this.accept(socket))
	this.server.on('error', error => this.handlers.onError?.(error))
  }
  start() {
    return new Promise((resolve, reject) => {
      this.server.once('error', reject)
      this.server.listen(0, '127.0.0.1', () => {
        this.server.removeListener('error', reject)
        resolve(`127.0.0.1:${this.server.address().port}`)
      })
    })
  }
  accept(socket) {
    let buffer = '', session = ''
    socket.setEncoding('utf8')
	socket.on('error', error => this.handlers.onError?.(error))
    socket.on('data', chunk => {
      buffer += chunk
	  if (Buffer.byteLength(buffer, 'utf8') > 64 * 1024) { socket.destroy(); return }
      while (buffer.includes('\n')) {
        const index = buffer.indexOf('\n'), line = buffer.slice(0, index); buffer = buffer.slice(index + 1)
        let frame
        try { frame = JSON.parse(line) } catch { socket.destroy(); return }
        if (!session) {
          const supplied = typeof frame.token === 'string' ? Buffer.from(frame.token) : Buffer.alloc(0)
          const expected = Buffer.from(this.token)
          if (frame.type !== 'hello' || supplied.length !== expected.length || !crypto.timingSafeEqual(supplied, expected) || !frame.session) { socket.destroy(); return }
          if (this.clients.has(frame.session)) { socket.destroy(); return }
          session = frame.session; this.clients.set(session, socket); socket.write(`${JSON.stringify({type:'ready', session})}\n`); this.handlers.onConnect?.(session); continue
        }
        if (frame.type === 'event' && frame.event) this.handlers.onEvent?.(session, frame.event)
        if (frame.type === 'response' && frame.id) {
          const pending = this.pending.get(frame.id)
          if (pending) { this.pending.delete(frame.id); frame.ok ? pending.resolve(frame) : pending.reject(new Error(frame.error || 'stream request failed')) }
        }
      }
    })
    socket.on('close', () => {
      if (session && this.clients.get(session) === socket) this.clients.delete(session)
      for (const [id, pending] of this.pending) {
        if (pending.session !== session) continue
        this.pending.delete(id)
        pending.reject(new Error('stream session disconnected'))
      }
      if (session) this.handlers.onDisconnect?.(session)
    })
  }
  request(session, method, stream) {
    const socket = this.clients.get(session)
    if (!socket) return Promise.reject(new Error('stream session is no longer connected'))
    const id = `request-${++this.sequence}`
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error('stream request timed out')) }, 3000)
      this.pending.set(id, {session, resolve: value => { clearTimeout(timer); resolve(value) }, reject: error => { clearTimeout(timer); reject(error) }})
      socket.write(`${JSON.stringify({type:'request', id, method, stream})}\n`)
    })
  }
  async close() {
    for (const socket of this.clients.values()) socket.destroy()
    this.clients.clear()
    await new Promise(resolve => this.server.close(() => resolve()))
  }
}

module.exports = { StreamControlServer }
