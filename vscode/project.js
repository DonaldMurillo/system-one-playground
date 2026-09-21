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

function readExternalModules(root) {
  const result = []
  const configHome = process.env.SOS_CONFIG_HOME || (process.platform === 'darwin'
    ? path.join(os.homedir(), 'Library', 'Application Support', 'sysonescript')
    : process.platform === 'win32'
      ? path.join(process.env.APPDATA || path.join(os.homedir(), 'AppData', 'Roaming'), 'sysonescript')
      : path.join(process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config'), 'sysonescript'))
  for (const filename of [path.join(configHome, 'config.toml'), path.join(root, 'sos.toml')]) {
    let source
    try { source = fs.readFileSync(filename, 'utf8') } catch (error) {
      if (error?.code === 'ENOENT') continue
      throw new Error(`Cannot read ${filename}: ${error.message}`)
    }
    const base = path.dirname(filename)
    let current
    for (const raw of source.split(/\r?\n/)) {
      let quote = ''
      let escaped = false
      let comment = raw.length
      for (let index = 0; index < raw.length; index += 1) {
        const char = raw[index]
        if (escaped) { escaped = false; continue }
        if (quote === '"' && char === '\\') { escaped = true; continue }
        if (quote) { if (char === quote) quote = ''; continue }
        if (char === '"' || char === "'") { quote = char; continue }
        if (char === '#') { comment = index; break }
      }
      const line = raw.slice(0, comment).trim()
      if (/^\[\[\s*module\.external\s*\]\]$/.test(line)) {
        if (current && (!current.path || !current.definition)) throw new Error(`Invalid external module registration in ${filename}`)
        current = { base }; result.push(current); continue
      }
      if (line.startsWith('[')) {
        if (current && (!current.path || !current.definition)) throw new Error(`Invalid external module registration in ${filename}`)
        current = undefined; continue
      }
      if (!current) continue
      const match = line.match(/^(path|definition)\s*=\s*(["'])(.*?)\2\s*$/)
      if (match) current[match[1]] = match[3]
    }
    if (current && (!current.path || !current.definition)) throw new Error(`Invalid external module registration in ${filename}`)
    for (const array of source.matchAll(/(?:module\.)?external\s*=\s*\[([\s\S]*?)\]/g)) {
      for (const table of array[1].matchAll(/\{([\s\S]*?)\}/g)) {
        const item = { base }
        for (const field of table[1].matchAll(/\b(path|definition)\s*=\s*(["'])(.*?)\2/g)) item[field[1]] = field[3]
        if (item.path && item.definition) result.push(item)
      }
    }
  }
  const unique = new Map()
  for (const item of result.filter(item => item.path && item.definition)) unique.set(item.path, { path:item.path, definition:path.resolve(item.base,item.definition) })
  return [...unique.values()]
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

module.exports = { compareVersions, discoverEntrypoints, findProjectRoot, isDirectory, parseVersionLine, readExternalModules, readHelpers, relativeScript, resolveProjectEntrypoint, walkScripts }
