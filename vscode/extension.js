const path = require('node:path')
const fs = require('node:fs')
const vscode = require('vscode')
const { LspClient } = require('./lsp-client')

const LANGUAGE_ID = 'sos'
const DOCUMENT_SELECTOR = [{ language: LANGUAGE_ID }]

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
  return env
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
    'keyword', 'variable', 'parameter', 'function', 'type', 'namespace', 'string', 'number', 'comment', 'macro', 'enumMember',
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

function activate(context) {
  const output = vscode.window.createOutputChannel('SysOneScript')
  const diagnostics = vscode.languages.createDiagnosticCollection('sysonescript')
  extensionState = { output, diagnostics, client: null, ready: null, opened: new Set(), extensionPath: context.extensionPath }
  context.subscriptions.push(output, diagnostics)

  registerDocumentSync(context)
  registerLanguageProviders(context)
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.analyze', analyzeActiveDocument))
  context.subscriptions.push(vscode.commands.registerCommand('sysonescript.restartServer', restartServer))
  context.subscriptions.push(vscode.commands.registerCommand('sos.analyze', analyzeActiveDocument))
  context.subscriptions.push(vscode.workspace.onDidChangeConfiguration(event => {
    if (event.affectsConfiguration('sysonescript.server') || event.affectsConfiguration('sysonescript.trace.server')) {
      restartServer().catch(() => {})
    }
  }))

  startServer().catch(() => {})
}

async function deactivate() {
  const state = extensionState
  extensionState = undefined
  if (state?.client) await state.client.stop()
}

module.exports = { activate, deactivate }
