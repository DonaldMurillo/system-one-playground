#!/usr/bin/env node
'use strict'

// A complete sos-plugin/1 producer. The deterministic price function keeps the
// example offline; a production adapter would feed emit() from a broker socket.
const readline = require('node:readline')
const input = readline.createInterface({input: process.stdin, crlfDelay: Infinity})
const streams = new Map()

function send(message) { process.stdout.write(JSON.stringify(message) + '\n') }
function notify(method, params) { send({jsonrpc: '2.0', method, params}) }

function finish(state) {
  if (state.ended) return
  state.ended = true
  if (state.timer) clearInterval(state.timer)
  streams.delete(state.id)
  notify('stream.end', {streamId: state.id, lastSequence: state.sequence - 1})
}

function emit(state, value) {
  if (state.ended || state.credit === 0) return false
  notify('stream.item', {streamId: state.id, sequence: state.sequence, value})
  state.sequence++
  state.credit--
  return true
}

function pumpFinite(state) {
  while (!state.ended && state.credit > 0 && state.sequence < state.count) {
    const value = state.values
      ? state.values[state.sequence]
      : {sequence: state.sequence, message: `node event ${state.sequence}`}
    emit(state, value)
  }
  if (state.sequence === state.count) finish(state)
}

function marketPrice(second) {
  const changes = [0.18, -0.07, 0.31, 0.12, -0.42, 0.26, 0.19, -0.11, 0.35, 0.09]
  let price = 100
  for (let index = 0; index <= second; index++) price += changes[index % changes.length]
  return {price: Number(price.toFixed(2)), change: changes[second % changes.length]}
}

function startMarket(state) {
  state.timer = setInterval(() => {
    if (state.ended || state.credit === 0) return
    const movement = marketPrice(state.sequence)
    emit(state, {symbol: state.symbol, second: state.sequence + 1, ...movement})
    if (state.sequence === state.count) finish(state)
  }, state.interval)
}

function openStream(request) {
  const {action, arguments: args = {}, credit} = request.params
  if (!Number.isInteger(credit) || credit < 1) throw new Error('stream credit must be a positive integer')
  const state = {id: `stream-${request.id}`, sequence: 0, credit, ended: false, timer: null}
  if (action === 'finite') {
    if (!Number.isInteger(args.count) || args.count < 0) throw new Error('count must be a nonnegative integer')
    state.count = args.count
    streams.set(state.id, state)
    send({jsonrpc: '2.0', id: request.id, result: {streamId: state.id, itemType: 'Event'}})
    pumpFinite(state)
    return
  }
  if (action === 'statuses') {
    Object.assign(state, {values: ['queued', 'queued', 'running'], count: 3})
    streams.set(state.id, state)
    send({jsonrpc: '2.0', id: request.id, result: {streamId: state.id, itemType: 'text'}})
    pumpFinite(state)
    return
  }
  if (action === 'watch_market') {
    if (typeof args.symbol !== 'string' || !Number.isInteger(args.updates) || args.updates < 1 ||
        !Number.isInteger(args.every_milliseconds) || args.every_milliseconds < 1) {
      throw new Error('watch_market requires symbol text and positive integer updates and every_milliseconds')
    }
    Object.assign(state, {symbol: args.symbol, count: args.updates, interval: args.every_milliseconds})
    streams.set(state.id, state)
    send({jsonrpc: '2.0', id: request.id, result: {streamId: state.id, itemType: 'MarketTick'}})
    startMarket(state)
    return
  }
  send({jsonrpc: '2.0', id: request.id, error: {code: -32601, message: `unknown streaming action ${action}`}})
}

function handle(message) {
  if (message.method === 'initialize') {
    const p = message.params
    send({jsonrpc: '2.0', id: message.id, result: {protocol: p.protocol, module: p.module, version: p.version, definitionDigest: p.definitionDigest}})
    return
  }
  if (message.method === 'stream.open') return openStream(message)
  if (message.method === 'stream.credit') {
    const state = streams.get(message.params?.streamId)
    if (!state || state.ended) return
    state.credit += message.params.credit
    if (state.symbol === undefined) pumpFinite(state)
    return
  }
  if (message.method === 'stream.cancel') {
    const state = streams.get(message.params?.streamId)
    if (state) finish(state)
    return
  }
  if (message.id !== undefined) send({jsonrpc: '2.0', id: message.id, error: {code: -32601, message: 'method not found'}})
}

input.on('line', line => {
  try { handle(JSON.parse(line)) }
  catch (error) { process.stderr.write(`${error.stack || error}\n`); process.exitCode = 1; input.close() }
})
input.on('close', () => { for (const state of streams.values()) finish(state) })
