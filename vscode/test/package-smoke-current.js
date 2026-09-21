const { execFileSync } = require('node:child_process')
const path = require('node:path')

const manifest = require('../package.json')
let supplied = process.argv.slice(2)
if (supplied[0] === '--') supplied = supplied.slice(1)
if (supplied.length === 0) supplied = [`${manifest.name}-${manifest.version}.vsix`]

execFileSync(process.execPath, [path.join(__dirname, 'package-smoke.js'), ...supplied], {
  cwd: path.resolve(__dirname, '..'),
  stdio: 'inherit'
})
