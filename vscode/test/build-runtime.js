const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const path = require('node:path')

const root = path.resolve(__dirname, '..', '..')
const bin = path.join(root, 'vscode', 'bin')
const version = require('../package.json').version
fs.rmSync(bin, { recursive: true, force: true })
fs.mkdirSync(bin, { recursive: true })
execFileSync('go', ['generate', './sos', './internal/sosbuild'], { cwd: root, stdio: 'inherit' })
execFileSync('go', ['build', '-trimpath', '-ldflags', `-X github.com/DonaldMurillo/system-one-playground/sos.Version=${version} -X github.com/DonaldMurillo/system-one-playground/sos.ReleaseMarker=SysOneScriptVersion=${version}`, '-o', path.join(bin, process.platform === 'win32' ? 'sos.exe' : 'sos'), './cmd/sos'], { cwd: root, stdio: 'inherit' })
