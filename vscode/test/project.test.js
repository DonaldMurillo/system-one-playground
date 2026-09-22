const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const test = require('node:test')

const { appendScriptArguments, compareVersions, discoverEntrypoints, findProjectRoot, parseVersionLine, readExternalModules, readHelpers, relativeScript, resolveProjectEntrypoint, walkScripts } = require('../project')

function fixture() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'sysonescript-project-'))
	const configHome = path.join(root, '.test-config')
	fs.mkdirSync(configHome)
	process.env.SOS_CONFIG_HOME = configHome
  fs.mkdirSync(path.join(root, 'src'), { recursive: true })
  fs.mkdirSync(path.join(root, '.vscode'))
  fs.writeFileSync(path.join(root, 'sos.toml'), 'version = 1\n')
  fs.writeFileSync(path.join(root, 'src', 'main.sos'), 'command app:\n  show "ok"\n')
  fs.writeFileSync(path.join(root, 'src', 'helper.sos'), 'show "helper"\n')
  fs.writeFileSync(path.join(root, '.vscode', 'sysonescript.json'), JSON.stringify({ helpers: [{ name: 'Build', command: 'build', args: ['src/main.sos', '--output', 'bin/app'] }] }))
  return root
}

test('finds project roots from files and nested folders', () => {
  const root = fixture()
  assert.equal(findProjectRoot(path.join(root, 'src', 'main.sos')), root)
  assert.equal(findProjectRoot(path.join(root, 'src')), root)
})

test('discovers stable script entrypoints and relative paths', () => {
  const root = fixture()
  const entries = discoverEntrypoints(root)
  assert.equal(relativeScript(root, entries[0]), 'src/main.sos')
  assert.deepEqual(walkScripts(root).map(file => relativeScript(root, file)), ['src/helper.sos', 'src/main.sos'])
})

test('resolves project actions without asking for an entrypoint', () => {
  const root = fixture()
  assert.equal(relativeScript(root, resolveProjectEntrypoint(root)), 'src/main.sos')
  assert.equal(relativeScript(root, resolveProjectEntrypoint(root, 'src/helper.sos')), 'src/helper.sos')
  assert.equal(relativeScript(root, resolveProjectEntrypoint(root, '../outside.sos')), 'src/main.sos')
})

test('passes project command arguments after the runner boundary', () => {
  assert.deepEqual(appendScriptArguments(['run', 'main.sos'], []), ['run', 'main.sos'])
  assert.deepEqual(
    appendScriptArguments(['run', '--save-resolution', 'result.json', 'main.sos'], ['report', 'tickets.json', '--output', 'reports']),
    ['run', '--save-resolution', 'result.json', 'main.sos', '--', 'report', 'tickets.json', '--output', 'reports'],
  )
})

test('reads project helper actions without accepting malformed entries', () => {
  const root = fixture()
  assert.deepEqual(readHelpers(root), [{ name: 'Build', command: 'build', args: ['src/main.sos', '--output', 'bin/app'], cwd: '', description: '' }])
  fs.writeFileSync(path.join(root, '.vscode', 'sysonescript.json'), '{not json')
  assert.deepEqual(readHelpers(root), [])
})

test('discovers external module definitions without launching them', () => {
  const root = fixture()
  fs.writeFileSync(path.join(root, 'sos.toml'), 'version=1\n[[module.external]]\npath="local/weather"\ndefinition="modules/weather/module.sos.toml"\n')
  assert.deepEqual(readExternalModules(root), [{ path:'local/weather', definition:path.join(root,'modules/weather/module.sos.toml') }])
})

test('external module discovery accepts TOML literal strings and hashes inside values', () => {
  const root = fixture()
  fs.writeFileSync(path.join(root, 'sos.toml'), `version=1
[[module.external]]
path='local/hash#module' # retained inside the value
definition='modules/hash#module/module.sos.toml'
`)
  assert.deepEqual(readExternalModules(root), [{
    path: 'local/hash#module',
    definition: path.join(root, 'modules', 'hash#module', 'module.sos.toml'),
  }])
})

test('external module discovery accepts inline TOML registration arrays', () => {
  const root = fixture()
  fs.writeFileSync(path.join(root, 'sos.toml'), `version=1
[module]
external = [
  { path = "local/inline", definition = "modules/inline/module.sos.toml" }
]
`)
  assert.deepEqual(readExternalModules(root), [{
    path: 'local/inline',
    definition: path.join(root, 'modules', 'inline', 'module.sos.toml'),
  }])
})

test('external module discovery includes global registrations with project overrides', () => {
  const root = fixture()
  fs.writeFileSync(path.join(process.env.SOS_CONFIG_HOME, 'config.toml'), `version=1
[[module.external]]
path="global/tool"
definition="modules/tool/module.sos.toml"
`)
  const modules = readExternalModules(root)
  assert.deepEqual(modules, [{
    path: 'global/tool',
    definition: path.join(process.env.SOS_CONFIG_HOME, 'modules', 'tool', 'module.sos.toml'),
  }])
})

test('external module discovery reports incomplete registrations', () => {
  const root = fixture()
  fs.writeFileSync(path.join(root, 'sos.toml'), '[[ module.external ]]\npath = "broken/module"\n')
  assert.throws(() => readExternalModules(root), /Invalid external module registration/)
})

test('maps SysOneScript operators to keyword styling across themes', () => {
  const manifest = require('../package.json')
  const operatorType = manifest.contributes.semanticTokenTypes.find(item => item.id === 'sosOperator')
  assert.equal(operatorType.superType, 'keyword')
  const scopes = manifest.contributes.semanticTokenScopes[0].scopes.sosOperator
  assert.deepEqual(scopes, ['keyword.control.operator.sos'])
  const grammar = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'syntaxes', 'sos.tmLanguage.json'), 'utf8'))
  assert.equal(grammar.repository.keywords.patterns[1].name, 'keyword.control.operator.sos')
  assert.equal(grammar.repository.keywords.patterns[2].name, 'keyword.control.operator.sos')
})

test('keeps project actions in the panel instead of duplicating title buttons', () => {
  const manifest = require('../package.json')
  const projectTitles = manifest.contributes.menus['view/title'].filter(item => item.when.includes('sysonescript.project')).map(item => item.command)
  assert.deepEqual(projectTitles, ['sysonescript.refresh'])
  assert.deepEqual(manifest.contributes.menus['view/title'].filter(item => item.when.includes('sysonescript.streams')).map(item => item.command), ['sysonescript.refreshStreams', 'sysonescript.showStreamsOutput'])
})

test('contributes an additive language icon for active icon themes', () => {
  const manifest = require('../package.json')
  const language = manifest.contributes.languages.find(item => item.id === 'sos')
  assert.deepEqual(language.icon, {
    light: './icons/sysonescript-file.svg',
    dark: './icons/sysonescript-file.svg'
  })
})

test('uses the dedicated SysOneScript glyph in VS Code chrome', () => {
  const manifest = require('../package.json')
  const container = manifest.contributes.viewsContainers.activitybar.find(item => item.id === 'sysonescript')
  assert.equal(container.icon, './icons/sysonescript-glyph.svg')
  const glyph = fs.readFileSync(path.join(__dirname, '..', 'icons', 'sysonescript-glyph.svg'), 'utf8')
  assert.match(glyph, /fill="currentColor"/)
  assert.doesNotMatch(glyph, /<rect/)
})

test('contributes native onboarding and exposes its actions in the command palette', () => {
  const manifest = require('../package.json')
  const walkthrough = manifest.contributes.walkthroughs.find(item => item.id === 'sysonescript.getStarted')
  assert.ok(walkthrough)
  assert.deepEqual(walkthrough.steps.map(step => step.id), [
    'sysonescript.getStarted.openPanel',
    'sysonescript.getStarted.run',
    'sysonescript.getStarted.debug',
    'sysonescript.getStarted.jev',
    'sysonescript.getStarted.cli',
  ])
  const palette = new Set(manifest.contributes.menus.commandPalette.map(item => item.command))
  for (const command of ['sysonescript.runProject', 'sysonescript.checkProject', 'sysonescript.buildProject', 'sysonescript.debugProject', 'sysonescript.setJevToken', 'sysonescript.openWelcome', 'sysonescript.getCli', 'sysonescript.updateCli', 'sysonescript.checkCliVersions', 'sysonescript.moduleOpen', 'sysonescript.moduleCheck', 'sysonescript.moduleDoctor']) {
    assert.ok(palette.has(command), `${command} should be in the Command Palette`)
  }
})

test('parses and compares CLI versions for extension sync warnings', () => {
  assert.equal(parseVersionLine('sos 0.6.0\n'), '0.6.0')
  assert.equal(parseVersionLine('sysone v0.7.1-beta.1'), '0.7.1-beta.1')
  assert.equal(parseVersionLine('not installed'), undefined)
  assert.equal(compareVersions('0.6.0', '0.5.9'), 1)
  assert.equal(compareVersions('0.6.0', '0.6.0'), 0)
  assert.equal(compareVersions('0.5.9', '0.6.0'), -1)
})
