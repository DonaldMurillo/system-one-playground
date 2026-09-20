const fs = require('node:fs')
const path = require('node:path')

function isDirectory(value) {
  try { return fs.statSync(value).isDirectory() } catch { return false }
}

function findProjectRoot(start) {
  if (!start) return undefined
  let current = fs.existsSync(start) && !isDirectory(start) ? path.dirname(start) : start
  current = path.resolve(current)
  while (true) {
    if (fs.existsSync(path.join(current, 'sos.toml'))) return current
    const parent = path.dirname(current)
    if (parent === current) return undefined
    current = parent
  }
}

function walkScripts(root) {
  const result = []
  const visit = directory => {
    let entries
    try { entries = fs.readdirSync(directory, { withFileTypes: true }) } catch { return }
    for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
      if (entry.name.startsWith('.') || entry.name === 'node_modules' || entry.name === 'bin' || entry.name === 'dist') continue
      const full = path.join(directory, entry.name)
      if (entry.isDirectory()) visit(full)
      else if (entry.isFile() && entry.name.endsWith('.sos')) result.push(full)
    }
  }
  visit(root)
  return result
}

function discoverEntrypoints(root) {
  const scripts = walkScripts(root)
  const marked = scripts.filter(file => {
    try {
      const source = fs.readFileSync(file, 'utf8')
      return /^\s*command\b/m.test(source) || /^\s*show\b/m.test(source)
    } catch { return false }
  })
  const candidates = marked.length ? marked : scripts
  const preferred = candidates.filter(file => ['main.sos', 'index.sos'].includes(path.basename(file)))
  return preferred.length ? preferred.concat(candidates.filter(file => !preferred.includes(file))) : candidates
}

function resolveProjectEntrypoint(root, configured = '') {
  if (!root) return undefined
  if (configured) {
    const candidate = path.resolve(root, configured)
    if (candidate.startsWith(path.resolve(root) + path.sep) && fs.existsSync(candidate) && !isDirectory(candidate) && candidate.endsWith('.sos')) return candidate
  }
  return discoverEntrypoints(root)[0]
}

function readHelpers(root) {
  const configPath = path.join(root, '.vscode', 'sysonescript.json')
  try {
    const parsed = JSON.parse(fs.readFileSync(configPath, 'utf8'))
    if (!Array.isArray(parsed.helpers)) return []
    return parsed.helpers.filter(item => item && typeof item.name === 'string' && typeof item.command === 'string').map(item => ({
      name: item.name,
      command: item.command,
      args: Array.isArray(item.args) ? item.args.map(String) : [],
      cwd: typeof item.cwd === 'string' ? item.cwd : '',
      description: typeof item.description === 'string' ? item.description : '',
    }))
  } catch { return [] }
}

function relativeScript(root, file) {
  return path.relative(root, file).split(path.sep).join('/')
}

function parseVersionLine(value) {
  const match = String(value || '').match(/\b(?:sos|sysone)\s+v?(\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?)/i)
  return match?.[1]
}

function compareVersions(left, right) {
  const parse = value => String(value || '').replace(/^v/, '').split(/[.+-]/).slice(0, 3).map(part => Number.parseInt(part, 10) || 0)
  const a = parse(left)
  const b = parse(right)
  for (let i = 0; i < 3; i += 1) {
    if (a[i] < b[i]) return -1
    if (a[i] > b[i]) return 1
  }
  return 0
}

module.exports = { compareVersions, discoverEntrypoints, findProjectRoot, isDirectory, parseVersionLine, readHelpers, relativeScript, resolveProjectEntrypoint, walkScripts }
