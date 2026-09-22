'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const net = require('node:net')
const { StreamStore, canStopStream, traceLine } = require('../streams')
const { StreamControlServer } = require('../streams-server')

test('stream store preserves sessions and marks unterminated streams unknown', () => {
  const store = new StreamStore(); store.begin('run-1', {label:'Run main.sos', root:'/one'})
  store.apply('run-1', {id:'stream-1', event:'opened', state:'open', binding:'events', itemsReceived:0})
  store.apply('run-1', {id:'stream-1', event:'reading', state:'reading', itemsReceived:2})
  assert.equal(canStopStream(store.list()[0]), true)
  store.end('run-1', 1)
  assert.equal(store.list()[0].state, 'unknown')
  assert.match(traceLine('run-1', store.list()[0]), /stream-1 events .*received=2/)
})

test('older snapshots cannot regress a newer terminal event', () => {
  const store = new StreamStore(); store.begin('run-1')
  store.apply('run-1', {id:'stream-1', state:'completed', updatedAt:'2026-01-01T00:00:02Z'})
  store.replace('run-1', [{id:'stream-1', state:'reading', updatedAt:'2026-01-01T00:00:01Z'}])
  assert.equal(store.list()[0].state, 'completed')
})

test('stream store bounds ended session history without removing active sessions', () => {
  const store = new StreamStore()
  store.begin('active')
  for (let index = 0; index < 12; index++) {
    store.begin(`ended-${index}`)
    store.end(`ended-${index}`, 0)
  }
  store.begin('new-active')
  assert.equal(store.sessionsList().filter(item => item.status === 'ended').length, 10)
  assert.deepEqual(store.sessionsList().filter(item => item.status === 'running').map(item => item.session), ['active', 'new-active'])
})

test('control server authenticates events and correlates requests', async () => {
  const seen = []
  const server = new StreamControlServer({onEvent:(session,event)=>seen.push([session,event])})
  const address = await server.start()
  const socket = net.connect(Number(address.split(':')[1]), '127.0.0.1')
  socket.setEncoding('utf8')
  socket.write(JSON.stringify({type:'hello', token:server.token, session:'run-1'})+'\n')
  assert.equal(JSON.parse(await new Promise(resolve => socket.once('data', resolve))).type, 'ready')
  socket.write(JSON.stringify({type:'event', event:{id:'stream-1', state:'open'}})+'\n')
  await new Promise(resolve => setTimeout(resolve, 20))
  assert.equal(seen[0][0], 'run-1')
  socket.on('data', data => { const request=JSON.parse(data.trim()); socket.write(JSON.stringify({type:'response', id:request.id, ok:true, streams:[]})+'\n') })
  const response = await server.request('run-1', 'streams.snapshot')
  assert.equal(response.ok, true)
  socket.destroy(); await server.close()
})

test('control server rejects an in-flight request when its session disconnects', async () => {
  const server = new StreamControlServer()
  const address = await server.start()
  const socket = net.connect(Number(address.split(':')[1]), '127.0.0.1')
  socket.setEncoding('utf8')
  socket.write(JSON.stringify({type:'hello', token:server.token, session:'run-1'})+'\n')
  await new Promise(resolve => socket.once('data', resolve))
  socket.once('data', () => socket.destroy())
  await assert.rejects(server.request('run-1', 'streams.snapshot'), /disconnected/)
  await server.close()
})

test('control server rejects an invalid token before registering a session', async () => {
  const connected = []
  const server = new StreamControlServer({onConnect:session=>connected.push(session)})
  const address = await server.start()
  const socket = net.connect(Number(address.split(':')[1]), '127.0.0.1')
  socket.write(JSON.stringify({type:'hello', token:'wrong', session:'run-1'})+'\n')
  await new Promise(resolve => socket.once('close', resolve))
  assert.deepEqual(connected, [])
  await server.close()
})

test('control server refuses a second client claiming a live session', async () => {
  const server = new StreamControlServer()
  const address = await server.start()
  const port = Number(address.split(':')[1])
  const first = net.connect(port, '127.0.0.1')
  try {
    first.setEncoding('utf8')
    first.write(JSON.stringify({type:'hello', token:server.token, session:'run-1'})+'\n')
    await new Promise(resolve => first.once('data', resolve))
    const second = net.connect(port, '127.0.0.1')
    second.write(JSON.stringify({type:'hello', token:server.token, session:'run-1'})+'\n')
    await new Promise(resolve => second.once('close', resolve))
    assert.equal(server.clients.size, 1)
    assert.equal(first.destroyed, false)
  } finally {
    first.destroy()
    await server.close()
  }
})

async function waitFor(predicate, label) {
  const deadline = Date.now() + 2000
  while (!predicate()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${label}`)
    await new Promise(resolve => setTimeout(resolve, 10))
  }
}

test('trace line surfaces terminal reason, item type and source line', () => {
  const store = new StreamStore(); store.begin('run-1')
  store.apply('run-1', {id:'stream-1', event:'stopped', state:'stopped', binding:'events', itemType:'sysone.data.Record', producer:'events.tail', line:12, reason:'consumer cancelled', itemsReceived:3})
  const line = traceLine('run-1', store.list()[0])
  assert.match(line, /state=stopped/)
  assert.match(line, /\(sysone\.data\.Record\)/)
  assert.match(line, /line=12/)
  assert.match(line, /reason=consumer cancelled/)
  const quiet = traceLine('run-1', {id:'stream-2', state:'reading', reason:'ignored until terminal'})
  assert.doesNotMatch(quiet, /reason=/)
})

test('trace line exposes current and maximum derived key counts', () => {
  const line = traceLine('run-1', {id:'stream-1', state:'reading', policy:{keys:2, max_keys:5, pending:1, timers:2}})
  assert.match(line, /keys=2\/5 pending=1 timers=2/)
})

test('control server delivers a multi-megabyte frame', async () => {
  const seen = []
  const server = new StreamControlServer({onEvent:(session,event)=>seen.push([session,event])})
  const address = await server.start()
  const socket = net.connect(Number(address.split(':')[1]), '127.0.0.1')
  socket.on('error', () => {})
  socket.setEncoding('utf8')
  socket.write(JSON.stringify({type:'hello', token:server.token, session:'run-1'})+'\n')
  await new Promise(resolve => socket.once('data', resolve))
  socket.write(JSON.stringify({type:'event', event:{id:'stream-1', state:'open', padding:'x'.repeat(2 * 1024 * 1024)}})+'\n')
  await waitFor(() => seen.length === 1, 'large frame delivery')
  assert.equal(seen[0][0], 'run-1')
  assert.equal(seen[0][1].padding.length, 2 * 1024 * 1024)
  socket.destroy(); await server.close()
})

test('control server destroys a socket whose frame exceeds the 8 MiB limit', async () => {
  const errors = []
  const server = new StreamControlServer({onError:error=>errors.push(error)})
  const address = await server.start()
  const socket = net.connect(Number(address.split(':')[1]), '127.0.0.1')
  socket.on('error', () => {})
  socket.setEncoding('utf8')
  socket.write(JSON.stringify({type:'hello', token:server.token, session:'run-1'})+'\n')
  await new Promise(resolve => socket.once('data', resolve))
  socket.write(JSON.stringify({type:'event', event:{id:'stream-1', state:'open', padding:'x'.repeat(9 * 1024 * 1024)}})+'\n')
  await new Promise(resolve => socket.once('close', resolve))
  assert.ok(errors.some(error => /exceeded 8 MiB limit/.test(error.message)))
  await server.close()
})

test('control server refuses connections beyond the connection cap', async () => {
  const errors = []
  const server = new StreamControlServer({onError:error=>errors.push(error)})
  const port = Number((await server.start()).split(':')[1])
  const sockets = []
  for (let index = 0; index < 16; index++) {
    const socket = net.connect(port, '127.0.0.1')
    socket.on('error', () => {})
    sockets.push(socket)
  }
  await waitFor(() => server.sockets.size === 16, 'sixteen accepted connections')
  const extra = net.connect(port, '127.0.0.1')
  extra.on('error', () => {})
  await new Promise(resolve => extra.once('close', resolve))
  assert.ok(errors.some(error => /connection limit/.test(error.message)))
  sockets[0].setEncoding('utf8')
  sockets[0].write(JSON.stringify({type:'hello', token:server.token, session:'run-1'})+'\n')
  assert.equal(JSON.parse(await new Promise(resolve => sockets[0].once('data', resolve))).type, 'ready')
  assert.equal(server.sockets.size, 16)
  for (const socket of sockets) socket.destroy()
  await server.close()
})

test('control server reports dropped events from a bye frame exactly once', async () => {
  const byes = [], disconnects = []
  const server = new StreamControlServer({
    onBye:(session,dropped)=>byes.push([session,dropped]),
    onDisconnect:session=>disconnects.push(session),
  })
  const address = await server.start()
  const socket = net.connect(Number(address.split(':')[1]), '127.0.0.1')
  socket.on('error', () => {})
  socket.setEncoding('utf8')
  socket.write(JSON.stringify({type:'hello', token:server.token, session:'run-1'})+'\n')
  await new Promise(resolve => socket.once('data', resolve))
  socket.write(JSON.stringify({type:'bye', dropped:7})+'\n')
  socket.write(JSON.stringify({type:'bye', dropped:9})+'\n')
  await waitFor(() => byes.length === 1, 'bye frame delivery')
  assert.deepEqual(byes, [['run-1', 7]])
  socket.destroy()
  await waitFor(() => disconnects.length === 1, 'normal close handling after bye')
  assert.deepEqual(disconnects, ['run-1'])
  await server.close()
})
