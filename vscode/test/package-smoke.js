const assert = require('node:assert/strict')
const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')

const packageRoot = path.resolve(__dirname, '..')
const manifest = require('../package.json')
const defaultVsix = `${manifest.name}-${manifest.version}.vsix`
const supplied = process.argv.slice(2).filter(name => name !== '--')
assert.ok(supplied.length === 0 || (supplied.length === 1 && supplied[0].endsWith('.vsix')), 'expected no arguments or exactly one VSIX path')
const vsix = supplied[0] || defaultVsix
const expectedTarget = process.env.SYSONESCRIPT_EXPECTED_TARGET
const expectedName = `${manifest.name}${expectedTarget ? `-${expectedTarget}` : ''}-${manifest.version}.vsix`
assert.equal(path.basename(vsix), expectedName, 'VSIX filename does not match the exact current version and expected target')
assert.ok(vsix, 'a VSIX path is required')
const vsixPath = path.isAbsolute(vsix) ? vsix : path.resolve(packageRoot, vsix)
execFileSync('unzip', ['-tq', vsixPath], { stdio: 'pipe' })
const listing = execFileSync('unzip', ['-Z1', vsixPath], { encoding: 'utf8' }).split(/\r?\n/)
for (const required of ['extension/extension.js', 'extension/semantic.js', 'extension/project.js', 'extension/lsp-client.js']) {
  assert.ok(listing.includes(required), `VSIX is missing ${required}`)
}
const binaries = listing.filter(name => /^extension\/bin\/sos(?:\.exe)?$/.test(name))
assert.equal(binaries.length, 1, `VSIX must contain exactly one target runtime; found ${binaries.join(', ')}`)
const extensionManifest = execFileSync('unzip', ['-p', vsixPath, 'extension/package.json'], { encoding: 'utf8' })
const packaged = JSON.parse(extensionManifest)
assert.equal(packaged.name, manifest.name)
assert.equal(packaged.publisher, manifest.publisher)
assert.equal(packaged.version, manifest.version)
const vsixManifest = execFileSync('unzip', ['-p', vsixPath, 'extension.vsixmanifest'], { encoding: 'utf8' })
const effectiveXml = vsixManifest.replace(/<!--[\s\S]*?-->/g, '')
const identities = [...effectiveXml.matchAll(/<Identity\s+([^>]*?)\s*\/?\s*>/g)]
assert.equal(identities.length, 1, 'VSIX manifest must contain exactly one effective Identity element')
const identity = {}
for (const match of identities[0][1].matchAll(/([A-Za-z_:][\w:.-]*)\s*=\s*"([^"]*)"/g)) {
  assert.ok(!(match[1] in identity), `duplicate Identity attribute ${match[1]}`)
  identity[match[1]] = match[2]
}
assert.equal(identity.Id, manifest.name)
assert.equal(identity.Version, manifest.version)
assert.equal(identity.Publisher, manifest.publisher)
assert.equal(identity.TargetPlatform, expectedTarget)
const temp = fs.mkdtempSync(path.join(require('node:os').tmpdir(), 'sysonescript-vsix-'))
try {
  execFileSync('unzip', ['-q', vsixPath, binaries[0], '-d', temp])
  const runtime = path.join(temp, binaries[0])
  const metadata = execFileSync('go', ['version', '-m', runtime], { encoding: 'utf8' })
  const expected = (expectedTarget || `${process.platform}-${process.arch}`).replace('win32-', 'windows-').replace('-x64', '-amd64')
  const [expectedOS, expectedArch] = expected.split('-')
  assert.match(metadata, new RegExp(`build\\tGOOS=${expectedOS}(?:\\r?\\n|$)`))
  assert.match(metadata, new RegExp(`build\\tGOARCH=${expectedArch}(?:\\r?\\n|$)`))
  execFileSync('go', ['run', '../cmd/sosverify', runtime, `SysOneScriptVersion=${manifest.version};SysOneScriptVersionEnd`], { cwd: packageRoot, stdio: 'pipe' })
  if (expectedOS !== 'windows') assert.notEqual(fs.statSync(runtime).mode & 0o111, 0, 'packaged Unix runtime is not executable')
} finally {
  fs.rmSync(temp, { recursive: true, force: true })
}
