import test from 'node:test'
import assert from 'node:assert/strict'
import { commandLeaves, effectiveInputs, findByPath } from './commands.js'

test('nested leaf selection retains root and inherited input scopes', () => {
  const leaf = { name: 'triage', inputs: [{ name: 'criterion', choices: ['urgent', 'all'] }] }
  const group = { name: 'tickets', inputs: [{ name: 'source' }], commands: [leaf] }
  const root = { name: 'support', inputs: [{ name: 'verbose', kind: 'switch', default: 'on' }], commands: [group] }
  const leaves = commandLeaves(root, [], [])
  assert.deepEqual(leaves.map(l => l.path), [['support', 'tickets', 'triage']])
  assert.equal(findByPath(root, ['support']), root)
  assert.equal(findByPath(root, leaves[0].path), leaf)
  assert.equal(findByPath(root, ['support', 'missing']), null)
  assert.deepEqual(effectiveInputs(leaf, [root, group]).map(i => i.name), ['verbose', 'source', 'criterion'])
})

test('root leaf scripts retain their own declared inputs', () => {
  const root = { name: 'hello', inputs: [{ name: 'name' }] }
  assert.deepEqual(commandLeaves(root, [], []).map(l => l.path), [['hello']])
  assert.deepEqual(effectiveInputs(root, []), root.inputs)
})
