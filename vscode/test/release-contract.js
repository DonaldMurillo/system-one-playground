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
const releaseReferenceFiles = ['README.md', 'vscode/README.md', 'docs/sysonescript-editor.md', 'docs-site/content/editor.md', 'vscode/walkthrough/cli.md']
for (const relative of releaseReferenceFiles) {
  const text = fs.readFileSync(path.join(root, relative), 'utf8')
  const vsixVersions = [...text.matchAll(/sysonescript-vscode(?:-[a-z0-9-]+)?-(\d+\.\d+\.\d+)\.vsix/g)].map(match => match[1])
  assert.ok(vsixVersions.every(version => version === manifest.version), `${relative} VSIX filenames must match the extension version`)
  const releaseVersions = [...text.matchAll(/vscode-v(\d+\.\d+\.\d+)/g)].map(match => match[1])
  assert.ok(releaseVersions.length > 0, `${relative} must contain at least one versioned release reference`)
  assert.ok(releaseVersions.every(version => version === manifest.version), `${relative} release references must match the extension version`)
}
