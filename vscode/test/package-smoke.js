const assert = require('node:assert/strict')
const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')

const vsix = process.argv.slice(2).find(name => name.endsWith('.vsix')) || fs.readdirSync('.').find(name => name.endsWith('.vsix'))
assert.ok(vsix, 'a VSIX path is required')
const listing = execFileSync('unzip', ['-Z1', path.resolve(vsix)], { encoding: 'utf8' }).split(/\r?\n/)
for (const required of ['extension/extension.js', 'extension/semantic.js', 'extension/project.js', 'extension/lsp-client.js']) {
  assert.ok(listing.includes(required), `VSIX is missing ${required}`)
}
const binaries = listing.filter(name => /^extension\/bin\/sos(?:\.exe)?$/.test(name))
assert.equal(binaries.length, 1, `VSIX must contain exactly one target runtime; found ${binaries.join(', ')}`)
