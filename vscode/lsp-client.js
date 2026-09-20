const { spawn } = require('node:child_process')

const DEFAULT_REQUEST_TIMEOUT_MS = 30_000

/**
 * Decode Content-Length framed JSON-RPC messages from an LSP process.
 * Keeping this separate from VS Code makes the transport easy to test.
 */
class LspMessageParser {
  constructor() {
    this.buffer = Buffer.alloc(0)
  }

  push(chunk) {
    if (!chunk || chunk.length === 0) return []
    this.buffer = Buffer.concat([this.buffer, Buffer.from(chunk)])
    const messages = []

    while (true) {
      let separator = this.buffer.indexOf(Buffer.from('\r\n\r\n'))
      let separatorLength = 4
      if (separator < 0) {
        separator = this.buffer.indexOf(Buffer.from('\n\n'))
        separatorLength = 2
      }
      if (separator < 0) break

      const header = this.buffer.subarray(0, separator).toString('ascii')
      const match = /^Content-Length:\s*(\d+)\s*$/im.exec(header)
      if (!match) throw new Error('LSP message is missing Content-Length')
      const length = Number(match[1])
      const bodyStart = separator + separatorLength
      if (this.buffer.length < bodyStart + length) break

      const body = this.buffer.subarray(bodyStart, bodyStart + length).toString('utf8')
      this.buffer = this.buffer.subarray(bodyStart + length)
      messages.push(JSON.parse(body))
    }

    return messages
  }
}

function frameMessage(message) {
  const body = Buffer.from(JSON.stringify(message), 'utf8')
  return Buffer.concat([
    Buffer.from(`Content-Length: ${body.length}\r\n\r\n`, 'ascii'),
    body,
  ])
}

/**
 * Minimal LSP client for the SysOneScript stdio server.
 *
 * The server does not require a full language-client framework: it uses full
 * document synchronization, never sends server requests, and exposes all
 * editor features through ordinary JSON-RPC requests and notifications.
 */
class LspClient {
  constructor(options = {}) {
    this.command = options.command || 'sysone'
    this.args = Array.isArray(options.args) ? options.args : ['lsp']
    this.cwd = options.cwd
    this.env = options.env
    this.onNotification = options.onNotification
    this.onStderr = options.onStderr
    this.onExit = options.onExit
    this.onTrace = options.onTrace
    this.spawnImpl = options.spawnImpl || spawn
    this.requestTimeoutMs = options.requestTimeoutMs || DEFAULT_REQUEST_TIMEOUT_MS
    this.nextId = 1
    this.pending = new Map()
    this.parser = new LspMessageParser()
    this.process = null
    this.ready = null
    this.intentionalStop = false
    this.initialized = false
  }

  start(initializeParams) {
    if (this.ready) return this.ready

    this.intentionalStop = false
    this.process = this.spawnImpl(this.command, this.args, {
      cwd: this.cwd,
      env: this.env,
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true,
    })

    this.process.stdout.on('data', chunk => {
      try {
        for (const message of this.parser.push(chunk)) this.handleMessage(message)
      } catch (error) {
        this.failAll(error)
      }
    })
    this.process.stderr.on('data', chunk => {
      this.onStderr?.(chunk.toString())
    })
    this.process.on('error', error => this.failAll(error))
    this.process.on('close', (code, signal) => {
      if (!this.intentionalStop) {
        this.failAll(new Error(`SysOneScript language server exited (${code ?? 'unknown'}${signal ? `, ${signal}` : ''})`))
      }
      this.onExit?.(code, signal)
    })

    this.ready = this.request('initialize', initializeParams)
      .then(result => {
        this.initialized = true
        this.notify('initialized', {})
        return result
      })
      .catch(error => {
        this.ready = null
        throw error
      })
    return this.ready
  }

  request(method, params) {
    if (!this.process || !this.process.stdin.writable) {
      return Promise.reject(new Error('SysOneScript language server is not running'))
    }
    const id = this.nextId++
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id)
        reject(new Error(`LSP request timed out: ${method}`))
      }, this.requestTimeoutMs)
      this.pending.set(id, { resolve, reject, timer, method })
      this.send({ jsonrpc: '2.0', id, method, params })
    })
  }

  notify(method, params) {
    if (!this.process || !this.process.stdin.writable) return
    this.send({ jsonrpc: '2.0', method, params })
  }

  send(message) {
    this.onTrace?.('client', message)
    this.process.stdin.write(frameMessage(message))
  }

  handleMessage(message) {
    this.onTrace?.('server', message)
    if (message.method) {
      this.onNotification?.(message.method, message.params)
      return
    }
    if (message.id === undefined || message.id === null) return
    const pending = this.pending.get(message.id)
    if (!pending) return
    this.pending.delete(message.id)
    clearTimeout(pending.timer)
    if (message.error) {
      const error = new Error(message.error.message || `LSP request failed: ${pending.method}`)
      error.code = message.error.code
      error.data = message.error.data
      pending.reject(error)
      return
    }
    pending.resolve(message.result)
  }

  failAll(error) {
    for (const pending of this.pending.values()) {
      clearTimeout(pending.timer)
      pending.reject(error)
    }
    this.pending.clear()
  }

  async stop() {
    const child = this.process
    if (!child) return
    this.intentionalStop = true
    try {
      if (this.initialized && child.stdin.writable) {
        await Promise.race([
          this.request('shutdown', null),
          new Promise(resolve => setTimeout(resolve, 500)),
        ])
        this.notify('exit', null)
      }
    } catch {
      // The process may already have exited; cleanup below is still enough.
    }
    child.stdin.end()
    await new Promise(resolve => {
      let settled = false
      const finish = () => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        resolve()
      }
      const timer = setTimeout(() => {
        if (!child.killed) child.kill()
        finish()
      }, 500)
      child.once('close', finish)
    })
    this.failAll(new Error('SysOneScript language server stopped'))
    this.process = null
    this.ready = null
    this.initialized = false
  }
}

module.exports = { DEFAULT_REQUEST_TIMEOUT_MS, LspClient, LspMessageParser, frameMessage }
