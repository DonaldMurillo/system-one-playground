'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')

// The fallback grammar must carry the canonical filesystem vocabulary:
// observation verbs, traversal modifiers, explicit write policy, and the
// destructive-remove wording (spec: sysonescript-files-spec tooling).
test('fallback grammar recognizes filesystem construction syntax', () => {
  const grammar = fs.readFileSync(path.join(__dirname, '..', 'syntaxes', 'sos.tmLanguage.json'), 'utf8')
  for (const word of ['list', 'walk', 'watch', 'inspect', 'remove', 'move', 'copy', 'check']) {
    assert.match(grammar, new RegExp(`\\b${word}\\b`), word)
  }
  for (const word of ['through', 'recursively', 'whether', 'exists', 'atomically', 'replacing', 'including', 'contents', 'empty', 'without', 'following', 'symbolic', 'links', 'deep', 'only']) {
    assert.match(grammar, new RegExp(`\\b${word}\\b`), word)
  }
})

// Reserved typed failures and the typed entry/change records must not be
// swallowed by the fallback grammar's identifier rule: they surface through
// the failure-contract scope, whose captures this test keeps aligned.
test('fallback grammar keeps failure contract scope for reserved file failures', () => {
  const grammar = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'syntaxes', 'sos.tmLanguage.json'), 'utf8'))
  const failureScope = grammar.repository.failureTypes
  assert.ok(failureScope, 'failureTypes repository missing')
  const source = JSON.stringify(grammar)
  for (const kind of ['FileNotFound', 'FileAlreadyExists', 'FilePermissionDenied', 'FileTooLarge', 'InvalidFileType', 'InvalidFilePath', 'FileTraversalLimitExceeded', 'FileWatchOverflow', 'FileSystemUnavailable']) {
    assert.doesNotMatch(source, new RegExp(`"\\b${kind}\\b"`), `${kind} must come from semantic tokens, not the fallback grammar`)
  }
})
