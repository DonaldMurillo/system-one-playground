const path = require('node:path')
const fs = require('node:fs')
const { spawn } = require('node:child_process')
const vscode = require('vscode')
const { LspClient } = require('./lsp-client')
const { compareVersions, discoverEntrypoints, findProjectRoot, parseVersionLine, readHelpers, relativeScript, resolveProjectEntrypoint } = require('./project')

const LANGUAGE_ID = 'sos'
const DOCUMENT_SELECTOR = [{ language: LANGUAGE_ID }]
const JEV_SECRET_KEY = 'sysonescript.jevApiKey'

let extensionState

function isSysOneScript(document) {
  return document.languageId === LANGUAGE_ID || document.fileName.toLowerCase().endsWith('.sos')
}

function position(value) {
  return new vscode.Position(value?.line || 0, value?.character || 0)
}

function range(value) {
  if (!value) return undefined
  return new vscode.Range(position(value.start), position(value.end))
}

function uri(value) {
  return value ? vscode.Uri.parse(value) : undefined
}

function markdown(value) {
  if (value === undefined || value === null) return undefined
  if (typeof value === 'string') return new vscode.MarkdownString(value)
  if (Array.isArray(value)) {
    const result = new vscode.MarkdownString()
    for (const part of value) {
      if (result.value) result.appendMarkdown('\n\n')
      result.appendMarkdown(markdownValue(part))
    }
    return result
  }
  return new vscode.MarkdownString(markdownValue(value))
}

function markdownValue(value) {
  if (typeof value === 'string') return value
  if (value?.value !== undefined) {
    if (value.language) return `\n\n\`\`\`${value.language}\n${value.value}\n\`\`\``
    return String(value.value)
  }
  return String(value)
}

function completionKind(kind) {
  const kinds = {
    1: vscode.CompletionItemKind.Text,
    2: vscode.CompletionItemKind.Method,
    3: vscode.CompletionItemKind.Function,
    4: vscode.CompletionItemKind.Constructor,
    5: vscode.CompletionItemKind.Field,
    6: vscode.CompletionItemKind.Variable,
    7: vscode.CompletionItemKind.Class,
    8: vscode.CompletionItemKind.Interface,
    9: vscode.CompletionItemKind.Module,
    10: vscode.CompletionItemKind.Property,
    13: vscode.CompletionItemKind.Value,
    14: vscode.CompletionItemKind.Keyword,
    15: vscode.CompletionItemKind.Snippet,
    17: vscode.CompletionItemKind.Reference,
    18: vscode.CompletionItemKind.File,
    19: vscode.CompletionItemKind.Folder,
    20: vscode.CompletionItemKind.Enum,
    21: vscode.CompletionItemKind.EnumMember,
    22: vscode.CompletionItemKind.Constant,
    23: vscode.CompletionItemKind.Struct,
    24: vscode.CompletionItemKind.Event,
    25: vscode.CompletionItemKind.Operator,
    26: vscode.CompletionItemKind.TypeParameter,
  }
  return kinds[kind] || vscode.CompletionItemKind.Text
}

function textEdit(value) {
  if (!value) return undefined
  return new vscode.TextEdit(range(value.range), value.newText || '')
}

function workspaceEdit(value) {
  if (!value) return undefined
  const edit = new vscode.WorkspaceEdit()
  for (const [documentUri, edits] of Object.entries(value.changes || {})) {
    edit.set(uri(documentUri), (edits || []).map(textEdit).filter(Boolean))
  }
  return edit
}

function command(value) {
  if (!value?.command) return undefined
  return { command: value.command, title: value.title || value.command, arguments: value.arguments || [] }
}

function location(value) {
  if (!value?.uri || !value.range) return undefined
  return new vscode.Location(uri(value.uri), range(value.range))
}

function configuration() {
  return vscode.workspace.getConfiguration('sysonescript')
}

function workspaceRoot() {
  const folder = vscode.workspace.workspaceFolders?.[0]
  return folder?.uri.fsPath || undefined
}

function serverCwd() {
  const configured = configuration().get('server.cwd', '')
  if (!configured) return workspaceRoot()
  const expanded = configured.replace(/\$\{workspaceFolder\}/g, workspaceRoot() || process.cwd())
  return path.isAbsolute(expanded) ? expanded : path.resolve(workspaceRoot() || process.cwd(), expanded)
}

function bundledServerCommand(extensionPath) {
  const executable = process.platform === 'win32' ? 'sos.exe' : 'sos'
  const bundled = path.join(extensionPath, 'bin', executable)
  return fs.existsSync(bundled) ? bundled : executable
}

function serverEnvironment() {
  const configured = configuration().get('server.env', {})
  const env = { ...process.env }
  for (const [name, value] of Object.entries(configured || {})) {
    if (value === null || value === undefined) delete env[name]
    else env[name] = String(value)
  }
  if (extensionState?.jevToken) env.TYPESAFE_API_KEY = extensionState.jevToken
  return env
}

function runnerCommand() {
  const configured = configuration().get('runner.command', '')
  if (configured) return configured
  const executable = process.platform === 'win32' ? 'sos.exe' : 'sos'
  const bundled = path.join(extensionState.extensionPath, 'bin', executable)
  return fs.existsSync(bundled) ? bundled : executable
}

function resourcePath(resource) {
  const value = resource?.fsPath || resource?.path || resource?.resource || resource
  return typeof value === 'string' ? value : undefined
}

function projectFor(resource) {
  const value = resourcePath(resource) || workspaceRoot()
  return findProjectRoot(value) || (value && fs.existsSync(value) && fs.statSync(value).isDirectory() ? value : workspaceRoot())
}

function fileFor(resource) {
  const value = resourcePath(resource)
  if (typeof value !== 'string') return undefined
  try { return fs.statSync(value).isDirectory() ? undefined : value } catch { return undefined }
}

function configuredEntry(root) {
  const configured = configuration().get('project.entry', '')
  if (!configured || !root) return undefined
  const candidate = resolveProjectEntrypoint(root, configured)
  return candidate && relativeScript(root, candidate) === configured.replace(/\\/g, '/') ? candidate : undefined
}

function activeProjectFile(root) {
  const file = fileFor(vscode.window.activeTextEditor?.document.uri)
  if (!file || !isSysOneScript({ fileName: file, languageId: LANGUAGE_ID })) return undefined
  return projectFor(file) === root ? file : undefined
}

function projectEntrypoint(resource) {
  const root = projectFor(resource)
  if (!root) {
    vscode.window.showErrorMessage('Open a SysOneScript project folder first.')
    return undefined
  }
  const file = configuredEntry(root) || activeProjectFile(root) || resolveProjectEntrypoint(root)
  if (!file) {
    vscode.window.showErrorMessage(`No .sos entrypoint was found in ${root}.`)
    return undefined
  }
  return { root, file }
}

function entrypointDescription(root) {
  const configured = configuredEntry(root)
  if (configured) return relativeScript(root, configured)
  const active = activeProjectFile(root)
  if (active) return `${relativeScript(root, active)} · active`
  const candidates = discoverEntrypoints(root)
  if (candidates.length === 1) return relativeScript(root, candidates[0])
  if (candidates.length > 1) return `${relativeScript(root, candidates[0])} · auto`
  return 'none found'
}

async function selectEntrypoint(resource) {
  const root = projectFor(resource)
  if (!root) return
  const candidates = discoverEntrypoints(root)
  const options = [
    { label: 'Auto-detect on each project action', description: 'Do not pin an entrypoint', file: '' },
    ...candidates.map(file => ({ label: relativeScript(root, file), description: 'Pin as the project entrypoint', file })),
  ]
  const picked = await vscode.window.showQuickPick(options, { placeHolder: 'Choose the project entrypoint' })
  if (!picked) return
  const value = picked.file ? relativeScript(root, picked.file) : ''
  await configuration().update('project.entry', value, vscode.ConfigurationTarget.Workspace)
  extensionState.tree.refresh()
}

function saveWorkspace() {
  return vscode.workspace.saveAll().catch(() => false)
}

function runInTerminal(args, cwd, label, commandOverride) {
  const output = extensionState.output
  const emitter = new vscode.EventEmitter()
  let child
  const write = text => {
    const value = String(text).replace(/\n/g, '\r\n')
    emitter.fire(value)
    output.append(String(text))
  }
  const pty = {
    name: 'SysOneScript',
    onDidWrite: emitter.event,
    open() {
      child = spawn(commandOverride || runnerCommand(), args, {
        cwd,
        env: serverEnvironment(),
        stdio: ['pipe', 'pipe', 'pipe'],
        windowsHide: true,
      })
      extensionState.processes.add(child)
      child.stdout.on('data', chunk => write(chunk.toString()))
      child.stderr.on('data', chunk => write(chunk.toString()))
      child.on('error', error => write(`SysOneScript could not start: ${error.message}\n`))
      child.on('close', code => {
        extensionState.processes.delete(child)
        write(`\n[${label} exited with code ${code ?? 'unknown'}]\n`)
        emitter.fire('\x1b[?25h')
      })
    },
    close() {
      if (child && !child.killed) child.kill()
    },
  }
  const terminal = vscode.window.createTerminal({ name: `SysOneScript: ${label}`, cwd, pty })
  terminal.show(true)
  return terminal
}

function setPanelRun(action, value) {
  if (!extensionState?.panelRuns) return
  extensionState.panelRuns.set(action, value)
  extensionState.tree.refresh()
}

function runPanelProcess(action, args, cwd, label, commandOverride) {
  const command = commandOverride || runnerCommand()
  const output = extensionState.runOutput
  output.clear()
  output.appendLine(`SysOneScript · ${label}`)
  output.appendLine(`$ ${path.basename(command)} ${args.join(' ')}`)
  output.appendLine('')
  output.show(true)
  setPanelRun(action, { status: 'running', label: `${label} · running` })
  const child = spawn(command, args, { cwd, env: serverEnvironment(), stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true })
  extensionState.processes.add(child)
  child.stdout.on('data', chunk => output.append(chunk.toString()))
  child.stderr.on('data', chunk => output.append(chunk.toString()))
  child.on('error', error => {
    output.appendLine(`SysOneScript could not start: ${error.message}`)
    setPanelRun(action, { status: 'failed', label: `${label} · failed` })
  })
  child.on('close', code => {
    extensionState.processes.delete(child)
    output.appendLine('')
    output.appendLine(`[${label} ${code === 0 ? 'completed' : `exited with code ${code ?? 'unknown'}`}]`)
    setPanelRun(action, { status: code === 0 ? 'success' : 'failed', label: `${label} · ${code === 0 ? 'done' : `exit ${code ?? 'unknown'}`}` })
  })
  return child
}

function showRunOutput() {
  extensionState.runOutput.show(true)
}

async function runProject(resource) {
  const selected = projectEntrypoint(resource)
  if (!selected) return
  await saveWorkspace()
  runPanelProcess('run', ['run', relativeScript(selected.root, selected.file)], selected.root, `Run ${relativeScript(selected.root, selected.file)}`)
}

async function runFile(resource) {
  const file = fileFor(resource) || fileFor(vscode.window.activeTextEditor?.document.uri)
  if (!file || !isSysOneScript({ fileName: file, languageId: LANGUAGE_ID })) {
    vscode.window.showInformationMessage('Choose a .sos file to run.')
    return
  }
  const root = projectFor(file)
  if (!root) return runProject(resource)
  await saveWorkspace()
  runInTerminal(['run', relativeScript(root, file)], root, `run ${relativeScript(root, file)}`)
}

async function checkProject(resource) {
  const selected = projectEntrypoint(resource)
  if (!selected) return
  await saveWorkspace()
  runPanelProcess('check', ['check', relativeScript(selected.root, selected.file)], selected.root, `Check ${relativeScript(selected.root, selected.file)}`)
}

async function buildProject(resource) {
  const selected = projectEntrypoint(resource)
  if (!selected) return
  const defaultOutput = path.join(selected.root, 'bin', path.basename(selected.file, '.sos'))
  await saveWorkspace()
  runPanelProcess('build', ['build', relativeScript(selected.root, selected.file), '--output', relativeScript(selected.root, defaultOutput)], selected.root, `Build ${relativeScript(selected.root, selected.file)}`)
}

async function explainFile(resource) {
  const file = fileFor(resource) || fileFor(vscode.window.activeTextEditor?.document.uri)
  if (!file) return
  const root = projectFor(file)
  if (!root) return
  await saveWorkspace()
  runInTerminal(['explain', relativeScript(root, file)], root, `explain ${relativeScript(root, file)}`)
}

async function stopProcesses() {
  for (const child of extensionState.processes) child.kill()
  extensionState.processes.clear()
  for (const state of extensionState.panelRuns.values()) {
    if (state.status === 'running') {
      state.status = 'stopped'
      state.label = `${state.label} · stopped`
    }
  }
  extensionState.tree.refresh()
}

async function debugFile(resource) {
  const file = fileFor(resource) || fileFor(vscode.window.activeTextEditor?.document.uri)
  if (!file) {
    vscode.window.showInformationMessage('Choose a .sos file to debug.')
    return
  }
  const root = projectFor(file)
  if (!root) return
  await saveWorkspace()
  let args = []
  try {
    const source = fs.readFileSync(file, 'utf8')
    if (/^\s*command\b/m.test(source)) {
      const entered = await vscode.window.showInputBox({
        prompt: 'Optional SysOneScript command and arguments',
        placeHolder: 'for example: report --output reports',
        value: '',
      })
      if (entered === undefined) return
      args = splitScriptArguments(entered)
    }
  } catch {
    // The debug adapter will report a more useful source error if the file
    // disappears between the editor action and launch.
  }
  await vscode.debug.startDebugging(vscode.workspace.getWorkspaceFolder(vscode.Uri.file(root)), {
    type: 'sysonescript',
    request: 'launch',
    name: `Debug ${path.basename(file)}`,
    program: file,
    cwd: root,
    args,
    stopOnEntry: false,
  })
}

function splitScriptArguments(value) {
  const result = []
  const pattern = /"([^"\\]*(?:\\.[^"\\]*)*)"|'([^']*)'|(\S+)/g
  let match
  while ((match = pattern.exec(value)) !== null) result.push(match[1] ?? match[2] ?? match[3])
  return result
}

async function debugProject(resource) {
  const selected = projectEntrypoint(resource)
  if (!selected) return
  await saveWorkspace()
  setPanelRun('debug', { status: 'running', label: `Debug ${relativeScript(selected.root, selected.file)} · running` })
  const started = await vscode.debug.startDebugging(vscode.workspace.getWorkspaceFolder(vscode.Uri.file(selected.root)), {
    type: 'sysonescript', request: 'launch', name: `Debug ${path.basename(selected.file)}`, program: selected.file, cwd: selected.root, args: [], stopOnEntry: false,
  })
  if (!started) setPanelRun('debug', { status: 'failed', label: 'Debug failed to start' })
}

async function setJevToken() {
  const token = await vscode.window.showInputBox({
    prompt: 'Enter the Jev API token (stored securely by VS Code)',
    password: true,
    ignoreFocusOut: true,
    validateInput: value => value.trim() ? undefined : 'A token is required.',
  })
  if (token === undefined) return
  await extensionState.secrets.store(JEV_SECRET_KEY, token.trim())
  extensionState.jevToken = token.trim()
  extensionState.tree.refresh()
  await restartServer()
  vscode.window.showInformationMessage('Jev token stored securely for SysOneScript.')
}

async function clearJevToken() {
  await extensionState.secrets.delete(JEV_SECRET_KEY)
  extensionState.jevToken = undefined
  extensionState.tree.refresh()
  await restartServer()
  vscode.window.showInformationMessage('Stored Jev token cleared. Environment or project .env values remain unchanged.')
}

async function showJevStatus() {
  const configured = Boolean(extensionState.jevToken || process.env.TYPESAFE_API_KEY || configuration().get('server.env.TYPESAFE_API_KEY'))
  vscode.window.showInformationMessage(configured ? 'Jev token is configured for SysOneScript.' : 'Jev token is not configured. Use SysOneScript: Set Jev Token.')
}

function jevStatus() {
  if (extensionState.jevToken) return 'configured in VS Code'
  if (process.env.TYPESAFE_API_KEY || configuration().get('server.env.TYPESAFE_API_KEY')) return 'configured in environment'
  return 'not configured'
}

async function selectIconTheme() {
  await vscode.commands.executeCommand('workbench.action.selectIconTheme')
}

async function openWelcome() {
  await vscode.commands.executeCommand(
    'workbench.action.openWalkthrough',
    'donaldmurillo.sysonescript-vscode#sysonescript.getStarted',
    false,
  )
}

async function getCli() {
  await vscode.env.openExternal(vscode.Uri.parse('https://github.com/DonaldMurillo/system-one-playground#install-the-standalone-cli'))
}

function readCommandVersion(command, args) {
  return new Promise(resolve => {
    let output = ''
    let settled = false
    const child = spawn(command, args, { env: process.env, windowsHide: true })
    const finish = value => {
      if (settled) return
      settled = true
      resolve(value)
    }
    child.stdout.on('data', chunk => { output += chunk })
    child.on('error', () => finish(undefined))
    child.on('close', code => finish(code === 0 ? parseVersionLine(output) : undefined))
  })
}

async function refreshCliVersions(notify = false) {
  const bundled = await readCommandVersion(bundledServerCommand(extensionState.extensionPath), ['version'])
  const external = await readCommandVersion(process.platform === 'win32' ? 'sysone.exe' : 'sysone', ['version'])
  extensionState.cliVersions = { extension: extensionState.extensionVersion, bundled, external }
  extensionState.tree.refresh()
  if (!notify || !external || !bundled || compareVersions(external, bundled) === 0 || extensionState.versionWarningShown) return
  extensionState.versionWarningShown = true
  const action = compareVersions(external, bundled) < 0 ? 'Update CLI' : 'Check Extension Updates'
  const selected = await vscode.window.showWarningMessage(`SysOneScript versions are out of sync: VS Code runtime ${bundled}, terminal CLI ${external}.`, action)
  if (selected === 'Update CLI') await updateCli()
  if (selected === 'Check Extension Updates') await vscode.commands.executeCommand('workbench.extensions.action.checkForUpdates')
}

async function updateCli() {
  await refreshCliVersions(false)
  if (!extensionState.cliVersions.external) {
    const selected = await vscode.window.showInformationMessage('The standalone SysOneScript CLI is not installed on PATH.', 'Install CLI')
    if (selected === 'Install CLI') await getCli()
    return
  }
  const comparison = compareVersions(extensionState.cliVersions.external, extensionState.cliVersions.bundled)
  if (comparison > 0) {
    const selected = await vscode.window.showWarningMessage(`The terminal CLI (${extensionState.cliVersions.external}) is newer than the VS Code runtime (${extensionState.cliVersions.bundled}).`, 'Check Extension Updates')
    if (selected === 'Check Extension Updates') await vscode.commands.executeCommand('workbench.extensions.action.checkForUpdates')
    return
  }
  if (comparison === 0) {
    vscode.window.showInformationMessage(`Terminal CLI ${extensionState.cliVersions.external} is already current with VS Code runtime ${extensionState.cliVersions.bundled}.`)
    return
  }
  const terminal = vscode.window.createTerminal({ name: 'SysOneScript Update', cwd: workspaceRoot() })
  terminal.show()
  terminal.sendText('sysone update', true)
}

async function runHelper(helper, resource) {
  const root = projectFor(resource)
  if (!root || !helper) return
  await saveWorkspace()
  const cwd = helper.cwd ? path.resolve(root, helper.cwd) : root
  const builtins = new Set(['run', 'check', 'build', 'fmt', 'explain', 'config', 'vocabulary'])
  if (builtins.has(helper.command)) runInTerminal([helper.command, ...helper.args], cwd, helper.name)
  else runInTerminal(helper.args, cwd, helper.name, helper.command)
}

class SysOneScriptItem extends vscode.TreeItem {
  constructor(label, collapsibleState, kind, resource, command, icon, description) {
    super(label, collapsibleState)
    this.kind = kind
    this.resource = resource
    this.contextValue = kind
    if (command) this.command = command
    if (icon) this.iconPath = new vscode.ThemeIcon(icon)
    if (description) this.description = description
  }
}

class SysOneScriptTreeProvider {
  constructor() { this.changed = new vscode.EventEmitter(); this.onDidChangeTreeData = this.changed.event }
  refresh() { this.changed.fire() }
  getTreeItem(element) { return element }
  getChildren(element) {
    const root = projectFor(element?.resource || workspaceRoot()) || workspaceRoot()
    if (!root) return [new SysOneScriptItem('Welcome & setup', vscode.TreeItemCollapsibleState.None, 'projectAction', undefined, { command: 'sysonescript.openWelcome', title: 'Open SysOneScript Welcome' }, 'sparkle')]
    if (!element) {
      const items = [
        new SysOneScriptItem(`Project · ${path.basename(root)}`, vscode.TreeItemCollapsibleState.None, 'projectStatus', root, undefined, 'rocket', `${entrypointDescription(root)} · ${root}`),
        new SysOneScriptItem('Welcome & setup', vscode.TreeItemCollapsibleState.None, 'projectAction', root, { command: 'sysonescript.openWelcome', title: 'Open SysOneScript Welcome' }, 'sparkle'),
      ]
      const appendAction = (action, label, command, icon) => {
        const state = extensionState.panelRuns.get(action)
        items.push(new SysOneScriptItem(label, vscode.TreeItemCollapsibleState.None, 'projectAction', root, { command, title: label, arguments: [root] }, icon, state?.label))
      }
      appendAction('run', 'Run project', 'sysonescript.runProject', 'play')
      appendAction('check', 'Check project', 'sysonescript.checkProject', 'check-all')
      appendAction('build', 'Build project', 'sysonescript.buildProject', 'package')
      appendAction('debug', 'Debug project', 'sysonescript.debugProject', 'debug-alt')
      items.push(
        new SysOneScriptItem('Show run output', vscode.TreeItemCollapsibleState.None, 'projectAction', root, { command: 'sysonescript.showRunOutput', title: 'Show SysOneScript Run Output' }, 'output'),
        new SysOneScriptItem('Stop processes', vscode.TreeItemCollapsibleState.None, 'projectAction', root, { command: 'sysonescript.stop', title: 'Stop SysOneScript Processes' }, 'stop-circle'),
        new SysOneScriptItem('Set Jev token', vscode.TreeItemCollapsibleState.None, 'jevStatus', root, { command: 'sysonescript.setJevToken', title: 'Set Jev Token' }, 'key', jevStatus()),
        new SysOneScriptItem('Get standalone CLI', vscode.TreeItemCollapsibleState.None, 'projectAction', root, { command: 'sysonescript.getCli', title: 'Get Standalone CLI' }, 'terminal'),
      )
      const versions = extensionState.cliVersions
      if (versions?.external && versions?.bundled) {
        const mismatch = compareVersions(versions.external, versions.bundled)
        items.push(new SysOneScriptItem(mismatch < 0 ? 'Update standalone CLI' : 'CLI version status', vscode.TreeItemCollapsibleState.None, 'projectAction', root, { command: 'sysonescript.updateCli', title: 'Update Standalone CLI' }, mismatch === 0 ? 'pass-filled' : 'warning', `VS Code ${versions.bundled} · terminal ${versions.external}${mismatch === 0 ? '' : ' · out of sync'}`))
      } else if (versions) {
        items.push(new SysOneScriptItem('CLI version status', vscode.TreeItemCollapsibleState.None, 'projectAction', root, { command: 'sysonescript.getCli', title: 'Get Standalone CLI' }, 'info', `VS Code ${versions.bundled || versions.extension} · terminal CLI not found`))
      }
      if (extensionState.jevToken) items.push(new SysOneScriptItem('Clear Jev token', vscode.TreeItemCollapsibleState.None, 'jevStatus', root, { command: 'sysonescript.clearJevToken', title: 'Clear Jev Token' }, 'trash'))
      const helpers = readHelpers(root)
      if (helpers.length) items.push(new SysOneScriptItem('Helpers & generators', vscode.TreeItemCollapsibleState.Expanded, 'helpers', root, undefined, 'tools'))
      return items
    }
    if (element.kind === 'helpers') return readHelpers(root).map(helper => new SysOneScriptItem(helper.name, vscode.TreeItemCollapsibleState.None, 'helper', root, { command: 'sysonescript.runHelper', title: helper.description || helper.name, arguments: [helper, root] }, 'play-circle'))
    return []
  }
}

function initializeParams() {
  const root = workspaceRoot()
  const rootUri = root ? vscode.Uri.file(root).toString() : null
  const folders = (vscode.workspace.workspaceFolders || []).map(folder => ({
    uri: folder.uri.toString(),
    name: folder.name,
  }))
  return {
    processId: process.pid,
    clientInfo: { name: 'SysOneScript VS Code extension' },
    rootUri,
    rootPath: root || null,
    workspaceFolders: folders.length ? folders : null,
    initializationOptions: { workspacePath: root || '' },
    capabilities: {
      workspace: { workspaceFolders: true },
      general: { positionEncodings: ['utf-16'] },
      textDocument: {
        completion: { completionItem: { snippetSupport: false } },
        publishDiagnostics: {},
        synchronization: { dynamicRegistration: false, willSave: false, didSave: false, willSaveWaitUntil: false },
      },
    },
    trace: 'off',
  }
}

function traceMessage(output, direction, message) {
  const level = configuration().get('trace.server', 'off')
  if (level === 'off') return
  if (level === 'messages') {
    output.appendLine(`${direction === 'client' ? '-->' : '<--'} ${message.method || (message.error ? 'response error' : 'response')}`)
    return
  }
  output.appendLine(`${direction === 'client' ? '-->' : '<--'} ${JSON.stringify(message)}`)
}

function clearDiagnostics() {
  extensionState?.diagnostics.clear()
}

function publishDiagnostics(params) {
  if (!params?.uri) return
  const diagnostics = (params.diagnostics || []).map(item => {
    const diagnostic = new vscode.Diagnostic(
      range(item.range),
      item.message || '',
      { 1: vscode.DiagnosticSeverity.Error, 2: vscode.DiagnosticSeverity.Warning, 3: vscode.DiagnosticSeverity.Information, 4: vscode.DiagnosticSeverity.Hint }[item.severity] || vscode.DiagnosticSeverity.Error,
    )
    diagnostic.source = item.source || 'sos'
    if (item.code !== undefined) diagnostic.code = item.code
    return diagnostic
  })
  extensionState.diagnostics.set(uri(params.uri), diagnostics)
}

async function startServer() {
  if (extensionState?.tokenReady) await extensionState.tokenReady
  const previous = extensionState?.client
  if (previous) await previous.stop()
  clearDiagnostics()

  const output = extensionState.output
  const configuredArgs = configuration().get('server.args', ['lsp'])
  const configuredCommand = configuration().get('server.command', '')
  const client = new LspClient({
    command: configuredCommand || bundledServerCommand(extensionState.extensionPath),
    args: Array.isArray(configuredArgs) ? configuredArgs : ['lsp'],
    cwd: serverCwd(),
    env: serverEnvironment(),
    onNotification(method, params) {
      if (method === 'textDocument/publishDiagnostics') publishDiagnostics(params)
      else if (method === 'window/logMessage' || method === 'window/showMessage') {
        if (params?.message) output.appendLine(`[${method}] ${params.message}`)
      }
    },
    onStderr(text) {
      output.append(text)
    },
    onExit(code, signal) {
      if (extensionState?.client === client && code !== 0) {
        output.appendLine(`SysOneScript language server exited with code ${code ?? 'unknown'}${signal ? ` (${signal})` : ''}`)
      }
    },
    onTrace(direction, message) {
      traceMessage(output, direction, message)
    },
  })
  const ready = client.start(initializeParams())
  extensionState.client = client
  extensionState.ready = ready
  extensionState.opened = new Set()

  ready.then(() => {
    for (const document of vscode.workspace.textDocuments) syncDocument(document)
  }).catch(error => {
    if (extensionState?.client !== client) return
    output.appendLine(`Unable to start SysOneScript language server: ${error.message}`)
    vscode.window.showErrorMessage(`SysOneScript language server could not start: ${error.message}`)
  })
  return ready
}

function syncDocument(document) {
  const state = extensionState
  if (!state || !isSysOneScript(document) || !state.client.initialized) return
  const documentUri = document.uri.toString()
  state.client.notify('textDocument/didOpen', {
    textDocument: {
      uri: documentUri,
      languageId: LANGUAGE_ID,
      version: document.version,
      text: document.getText(),
    },
  })
  state.opened.add(documentUri)
}

async function ensureDocument(document) {
  if (!isSysOneScript(document)) return false
  const state = extensionState
  if (!state) return false
  await state.ready
  if (!state.opened.has(document.uri.toString())) syncDocument(document)
  return true
}

async function request(method, params) {
  const state = extensionState
  if (!state) return undefined
  await state.ready
  return state.client.request(method, params)
}

function registerDocumentSync(context) {
  context.subscriptions.push(vscode.workspace.onDidOpenTextDocument(document => {
    const state = extensionState
    if (!state || !isSysOneScript(document)) return
    state.ready.then(() => syncDocument(document)).catch(() => {})
  }))
  context.subscriptions.push(vscode.workspace.onDidChangeTextDocument(event => {
    const state = extensionState
    if (!state || !isSysOneScript(event.document)) return
    state.ready.then(() => {
      if (!state.opened.has(event.document.uri.toString())) syncDocument(event.document)
      else state.client.notify('textDocument/didChange', {
        textDocument: { uri: event.document.uri.toString(), version: event.document.version },
        contentChanges: [{ text: event.document.getText() }],
      })
    }).catch(() => {})
  }))
  context.subscriptions.push(vscode.workspace.onDidCloseTextDocument(document => {
    const state = extensionState
    if (!state || !isSysOneScript(document)) return
    const documentUri = document.uri.toString()
    if (!state.opened.delete(documentUri)) return
    state.ready.then(() => state.client.notify('textDocument/didClose', { textDocument: { uri: documentUri } })).catch(() => {})
  }))
}

function registerLanguageProviders(context) {
  context.subscriptions.push(vscode.languages.registerCompletionItemProvider(DOCUMENT_SELECTOR, {
    async provideCompletionItems(document, position) {
      if (!await ensureDocument(document)) return undefined
      const result = await request('textDocument/completion', {
        textDocument: { uri: document.uri.toString() },
        position: { line: position.line, character: position.character },
      })
      const items = Array.isArray(result) ? result : result?.items || []
      return items.map(raw => {
        const item = new vscode.CompletionItem(raw.label || '', completionKind(raw.kind))
        if (raw.detail) item.detail = raw.detail
        if (raw.documentation) item.documentation = markdown(raw.documentation)
        if (raw.sortText) item.sortText = raw.sortText
        if (raw.filterText) item.filterText = raw.filterText
        if (raw.insertText) item.insertText = raw.insertText
        if (raw.textEdit) item.textEdit = textEdit(raw.textEdit)
        if (raw.additionalTextEdits) item.additionalTextEdits = raw.additionalTextEdits.map(textEdit).filter(Boolean)
        if (raw.command) item.command = command(raw.command)
        return item
      })
    },
  }, ':', ' '))

  context.subscriptions.push(vscode.languages.registerHoverProvider(DOCUMENT_SELECTOR, {
    async provideHover(document, position) {
      if (!await ensureDocument(document)) return undefined
      const result = await request('textDocument/hover', {
        textDocument: { uri: document.uri.toString() },
        position: { line: position.line, character: position.character },
      })
      if (!result?.contents) return undefined
      return new vscode.Hover(markdown(result.contents), range(result.range))
    },
  }))

  context.subscriptions.push(vscode.languages.registerDefinitionProvider(DOCUMENT_SELECTOR, {
    async provideDefinition(document, position) {
      if (!await ensureDocument(document)) return undefined
      const result = await request('textDocument/definition', {
        textDocument: { uri: document.uri.toString() },
        position: { line: position.line, character: position.character },
      })
      const values = Array.isArray(result) ? result : result ? [result] : []
      return values.map(location).filter(Boolean)
    },
  }))

  context.subscriptions.push(vscode.languages.registerDocumentFormattingEditProvider(DOCUMENT_SELECTOR, {
    async provideDocumentFormattingEdits(document) {
      if (!await ensureDocument(document)) return []
      const result = await request('textDocument/formatting', { textDocument: { uri: document.uri.toString() } })
      return (result || []).map(textEdit).filter(Boolean)
    },
  }))

  context.subscriptions.push(vscode.languages.registerFoldingRangeProvider(DOCUMENT_SELECTOR, {
    async provideFoldingRanges(document) {
      if (!await ensureDocument(document)) return []
      const result = await request('textDocument/foldingRange', { textDocument: { uri: document.uri.toString() } })
      return (result || []).map(item => new vscode.FoldingRange(item.startLine, item.endLine, vscode.FoldingRangeKind.Region))
    },
  }))

  const legend = new vscode.SemanticTokensLegend([
    'keyword', 'variable', 'parameter', 'function', 'type', 'namespace', 'string', 'number', 'comment', 'macro', 'enumMember', 'sosOperator',
  ])
  context.subscriptions.push(vscode.languages.registerDocumentSemanticTokensProvider(DOCUMENT_SELECTOR, {
    async provideDocumentSemanticTokens(document) {
      if (!await ensureDocument(document)) return undefined
      const result = await request('textDocument/semanticTokens/full', { textDocument: { uri: document.uri.toString() } })
      const builder = new vscode.SemanticTokensBuilder(legend)
      let line = 0
      let start = 0
      const data = result?.data || []
      for (let i = 0; i + 4 < data.length; i += 5) {
        const deltaLine = data[i]
        line += deltaLine
        start = deltaLine === 0 ? start + data[i + 1] : data[i + 1]
        const tokenType = legend.tokenTypes[data[i + 3]]
        if (tokenType) builder.push(new vscode.Range(line, start, line, start + data[i + 2]), tokenType)
      }
      return builder.build()
    },
  }, legend))

  context.subscriptions.push(vscode.languages.registerInlayHintsProvider(DOCUMENT_SELECTOR, {
    async provideInlayHints(document, hintRange) {
      if (!await ensureDocument(document)) return []
      const result = await request('textDocument/inlayHint', {
        textDocument: { uri: document.uri.toString() },
        range: {
          start: { line: hintRange.start.line, character: hintRange.start.character },
          end: { line: hintRange.end.line, character: hintRange.end.character },
        },
      })
      return (result || []).map(item => {
        const hint = new vscode.InlayHint(position(item.position), item.label || '', vscode.InlayHintKind.Type)
        hint.paddingLeft = true
        return hint
      })
    },
  }))

  context.subscriptions.push(vscode.languages.registerCodeLensProvider(DOCUMENT_SELECTOR, {
    async provideCodeLenses(document) {
      if (!await ensureDocument(document)) return []
      const result = await request('textDocument/codeLens', { textDocument: { uri: document.uri.toString() } })
      return (result || []).map(item => new vscode.CodeLens(range(item.range), command(item.command)))
    },
  }))

  context.subscriptions.push(vscode.languages.registerCodeActionsProvider(DOCUMENT_SELECTOR, {
    async provideCodeActions(document, actionRange, context) {
      if (!await ensureDocument(document)) return []
      const result = await request('textDocument/codeAction', {
        textDocument: { uri: document.uri.toString() },
        range: {
          start: { line: actionRange.start.line, character: actionRange.start.character },
          end: { line: actionRange.end.line, character: actionRange.end.character },
        },
        context: { diagnostics: context.diagnostics.map(diagnostic => ({
          range: {
            start: { line: diagnostic.range.start.line, character: diagnostic.range.start.character },
            end: { line: diagnostic.range.end.line, character: diagnostic.range.end.character },
          },
          severity: diagnostic.severity,
          message: diagnostic.message,
          source: diagnostic.source,
        })) },
      })
      return (result || []).map(raw => {
        const action = new vscode.CodeAction(raw.title || 'SysOneScript quick fix', vscode.CodeActionKind.QuickFix)
        action.edit = workspaceEdit(raw.edit)
        action.command = command(raw.command)
        return action
      })
    },
  }))
}

async function analyzeActiveDocument() {
  const document = vscode.window.activeTextEditor?.document
  if (!document || !isSysOneScript(document)) {
    vscode.window.showInformationMessage('Open a .sos file before asking SysOneScript to analyze it.')
    return
  }
  try {
    await ensureDocument(document)
    const result = await request('sos/analyze', { textDocument: { uri: document.uri.toString() } })
    extensionState.output.appendLine(`Analysis for ${document.uri.fsPath}`)
    extensionState.output.appendLine(JSON.stringify(result, null, 2))
    extensionState.output.show(true)
    vscode.window.showInformationMessage('SysOneScript analysis completed. See the SysOneScript output channel for details.')
  } catch (error) {
    extensionState.output.appendLine(`Analysis failed: ${error.message}`)
    extensionState.output.show(true)
    vscode.window.showErrorMessage(`SysOneScript analysis failed: ${error.message}`)
  }
}

async function restartServer() {
  try {
    await startServer()
    vscode.window.showInformationMessage('SysOneScript language server restarted.')
  } catch (error) {
    vscode.window.showErrorMessage(`SysOneScript language server could not start: ${error.message}`)
  }
}

class SysOneScriptDebugConfigurationProvider {
  resolveDebugConfiguration(folder, config) {
    const root = folder?.uri.fsPath || workspaceRoot()
    const result = { ...config }
    result.type = 'sysonescript'
    result.request = 'launch'
    result.name = result.name || 'Debug SysOneScript'
    result.cwd = result.cwd || root
    if (!result.program) {
      const editorFile = fileFor(vscode.window.activeTextEditor?.document.uri)
      if (editorFile) result.program = editorFile
      else {
        const entries = root ? discoverEntrypoints(root) : []
        if (entries.length === 1) result.program = entries[0]
      }
    }
    if (!result.program) {
      vscode.window.showErrorMessage('Choose a SysOneScript program to debug.')
      return undefined
    }
    return result
  }
}

class SysOneScriptDebugAdapterFactory {
  createDebugAdapterDescriptor(session) {
    const folder = session.workspaceFolder?.uri.fsPath || workspaceRoot()
    return new vscode.DebugAdapterExecutable(runnerCommand(), ['debug'], {
      cwd: folder,
      env: serverEnvironment(),
    })
  }
}

function activate(context) {
  const output = vscode.window.createOutputChannel('SysOneScript')
  const runOutput = vscode.window.createOutputChannel('SysOneScript Run')
  const diagnostics = vscode.languages.createDiagnosticCollection('sysonescript')
  const tree = new SysOneScriptTreeProvider()
  extensionState = {
    output,
    runOutput,
    diagnostics,
    tree,
    client: null,
    ready: null,
    opened: new Set(),
    processes: new Set(),
    panelRuns: new Map(),
    treeView: null,
    extensionPath: context.extensionPath,
    extensionVersion: context.extension.packageJSON.version,
    cliVersions: undefined,
    versionWarningShown: false,
    secrets: context.secrets,
    jevToken: undefined,
    tokenReady: context.secrets.get(JEV_SECRET_KEY).then(token => { extensionState.jevToken = token || undefined }),
  }
  context.subscriptions.push(output, runOutput, diagnostics)

  extensionState.treeView = vscode.window.createTreeView('sysonescript.project', { treeDataProvider: tree, showCollapseAll: true })
  context.subscriptions.push(extensionState.treeView)
  context.subscriptions.push(vscode.debug.registerDebugConfigurationProvider('sysonescript', new SysOneScriptDebugConfigurationProvider()))
  context.subscriptions.push(vscode.debug.registerDebugAdapterDescriptorFactory('sysonescript', new SysOneScriptDebugAdapterFactory()))
  context.subscriptions.push(vscode.debug.onDidTerminateDebugSession(session => {
    if (session.type === 'sysonescript') {
      output.appendLine('SysOneScript debugger session ended.')
      setPanelRun('debug', { status: 'success', label: 'Debug session ended' })
    }
  }))

  registerDocumentSync(context)
  registerLanguageProviders(context)
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.analyze', analyzeActiveDocument))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.restartServer', restartServer))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.selectEntrypoint', selectEntrypoint))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.runProject', runProject))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.runFile', runFile))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.checkProject', checkProject))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.buildProject', buildProject))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.explainFile', explainFile))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.debugFile', debugFile))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.debugProject', debugProject))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.stop', stopProcesses))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.showRunOutput', showRunOutput))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.setJevToken', setJevToken))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.clearJevToken', clearJevToken))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.showJevStatus', showJevStatus))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.selectIconTheme', selectIconTheme))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.openWelcome', openWelcome))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.getCli', getCli))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.updateCli', updateCli))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.checkCliVersions', () => refreshCliVersions(true)))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.refresh', () => tree.refresh()))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.runHelper', runHelper))
  context.subscriptions.push(vscode.commands.registerCommand('sos.analyze', analyzeActiveDocument))
  context.subscriptions.push(vscode.workspace.onDidChangeWorkspaceFolders(() => tree.refresh()))
  context.subscriptions.push(vscode.workspace.onDidCreateFiles(() => tree.refresh()))
  context.subscriptions.push(vscode.workspace.onDidDeleteFiles(() => tree.refresh()))
  context.subscriptions.push(vscode.workspace.onDidRenameFiles(() => tree.refresh()))
  context.subscriptions.push(vscode.window.onDidChangeActiveTextEditor(() => tree.refresh()))
  context.subscriptions.push(vscode.workspace.onDidChangeConfiguration(event => {
    if (event.affectsConfiguration('sysonescript.server') || event.affectsConfiguration('sysonescript.trace.server')) {
      restartServer().catch(() => {})
    }
  }))

  extensionState.tokenReady.then(() => startServer()).catch(() => {})
  refreshCliVersions(true).catch(() => {})
}

async function deactivate() {
  const state = extensionState
  extensionState = undefined
  if (state?.client) await state.client.stop()
  for (const child of state?.processes || []) child.kill()
}

module.exports = { activate, deactivate }
