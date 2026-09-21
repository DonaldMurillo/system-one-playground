const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')

const root = path.resolve(__dirname, '..', '..')
const manifest = require('../package.json')
const api = fs.readFileSync(path.join(root, 'sos', 'api.go'), 'utf8')
const runtime = api.match(/Version\s*=\s*"([^"]+)"/)?.[1]
assert.equal(manifest.version, runtime, 'extension and runtime versions must match')
const changelog = fs.readFileSync(path.join(root, 'vscode', 'CHANGELOG.md'), 'utf8')
assert.match(changelog, new RegExp(`^## ${manifest.version.replaceAll('.', '\\.')}$`, 'm'))
for (const relative of ['README.md', 'vscode/README.md', 'docs/sysonescript-editor.md', 'docs-site/content/editor.md']) {
  const text = fs.readFileSync(path.join(root, relative), 'utf8')
  assert.ok(!text.includes('sysonescript-vscode-0.2.0.vsix'), `${relative} contains a stale VSIX version`)
}
