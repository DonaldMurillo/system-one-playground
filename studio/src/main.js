import { installProjects } from './projects.js'
import { usageLine } from './usage.js'
import { commandLeaves, effectiveInputs, findByPath } from './commands.js'
import { normalizeCatalog, searchEntries, searchLibraries, enabledView, libraryView, libraryEnabled, importPreview, entrySignature, needsJev, diagnosticsFor } from './vocabulary.js'
import { installLanguageServices, diagnosticColumn } from './lsp.js'
import * as monaco from 'monaco-editor'
import editorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import { registerSOSLanguage } from './sos.js'
import './style.css'

// Monaco bundled locally (no CDN): vite compiles the worker to a same-origin asset.
self.MonacoEnvironment = { getWorker: () => new editorWorker() }

// Token arrives either injected into the document (desktop shell) or via the
// one-time localhost URL the browser shell prints (and we strip from history).
const TOKEN = window.__SOS_STUDIO_TOKEN__ || new URLSearchParams(location.search).get('token')
if (!TOKEN) {
  document.body.innerHTML =
    '<pre style="padding:2em;color:#e2788a;font-family:monospace">missing session token — start SysOneScript Studio via the sos-studio command or the desktop app</pre>'
  throw new Error('missing session token')
}
if (location.search) history.replaceState(null, '', location.pathname)

const $ = (id) => document.getElementById(id)
const state = { session: null, running: false, diagnostics: [], analyzing: false, analysisAbort: null, analysis: null }

let activeProjectPath = ''
let projects

async function api(path, body, method = 'POST', signal) {
  if (body && typeof body.source === 'string' && activeProjectPath.endsWith('.sos')) body = {...body, path: activeProjectPath}
  const res = await fetch(path, {
    method: body !== undefined || method === 'POST' ? 'POST' : 'GET',
    headers: { 'X-Studio-Token': TOKEN, 'Content-Type': 'application/json' },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    const msg = (data.error && data.error.message) || res.statusText
    const err = new Error(msg)
    err.status = res.status
    err.kind = data.error && data.error.kind
    err.analysis = data.analysis
    throw err
  }
  return data
}


registerSOSLanguage(monaco)
installLanguageServices(monaco, api)
monaco.editor.defineTheme('sos-dark', {
  base: 'vs-dark',
  inherit: true,
  semanticHighlighting: true,
  rules: [
    { token: 'keyword', foreground: 'e0a458' },
    { token: 'macro', foreground: 'b99ce8' },
    { token: 'enumMember', foreground: '82c7b6' },
    { token: 'connector', foreground: 'b08d5f' },
    { token: 'judgment', foreground: 'd98f4a', fontStyle: 'bold' },
    { token: 'type', foreground: '7fb3d0' },
    { token: 'literal', foreground: 'c792c7' },
    { token: 'string', foreground: '9dc197' },
    { token: 'number', foreground: 'c792c7' },
    { token: 'variable', foreground: 'd6dae1' },
    { token: 'parameter', foreground: 'e8c78e' },
    { token: 'function', foreground: '82c7b6' },
    { token: 'namespace', foreground: 'a9a2db' },
    { token: 'operator', foreground: 'e0a458' },
    { token: 'comment', foreground: '78828c', fontStyle: 'italic' }
  ],
  colors: {
    'editor.background': '#14161a',
    'editor.foreground': '#d6dae1',
    'editor.lineHighlightBackground': '#1a1d22',
    'editorLineNumber.foreground': '#5a6068',
    'editorLineNumber.activeForeground': '#e0a458',
    'editorCursor.foreground': '#e0a458',
    'editor.selectionBackground': '#3d3423',
    'editorIndentGuide.background1': '#23262c',
    'editorWidget.background': '#1a1d22',
    'editorWidget.border': '#23262c',
    'editorHoverWidget.background': '#1a1d22',
    'editorSuggestWidget.selectedBackground': '#2a2318',
    'focusBorder': '#e0a458'
  }
})

const editor = monaco.editor.create($('editor-host'), {
  value: '',
  language: 'sos',
  theme: 'sos-dark',
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
  fontSize: 13,
  lineHeight: 19,
  minimap: { enabled: false },
  automaticLayout: true,
  tabSize: 2,
  scrollBeyondLastLine: false,
  padding: { top: 8 },
  renderWhitespace: 'selection',
  fixedOverflowWidgets: true,
  'semanticHighlighting.enabled': true,
  inlayHints: { enabled: 'on', fontSize: 11, padding: true },
  quickSuggestions: { other: true, comments: false, strings: false }
})

editor.onDidChangeCursorPosition((e) => {
  $('cursor-pos').textContent = `Ln ${e.position.lineNumber}, Col ${e.position.column}`
})

function setMarkers(diagnostics) {
  state.diagnostics = diagnostics || []
  monaco.editor.setModelMarkers(editor.getModel(), 'sos', state.diagnostics.filter(d => d.severity !== 'information').map((d) => {
    const line=editor.getValue().split("\n")[Math.max(0,d.line-1)] || ""
    const column=diagnosticColumn(line,d.column)
    return ({
    startLineNumber: Math.max(1, d.line),
    startColumn: column,
    endLineNumber: Math.max(1, d.line),
    endColumn: column + 1,
    message: d.message,
    severity: d.severity === 'information' ? monaco.MarkerSeverity.Info : monaco.MarkerSeverity.Error
  })}))
  const n = state.diagnostics.filter(d => d.severity !== 'information').length
  const pending = state.diagnostics.length - n
  const badge = $('diag-count')
  badge.textContent = n ? String(n) : ''
  badge.className = 'count' + (n ? '' : ' is-clear')
  $('check-state').textContent = n ? `${n} issue${n === 1 ? '' : 's'}` : pending ? `${pending} awaiting interpretation` : 'no issues'
  renderDiagnostics()
}

function renderDiagnostics() {
  const ul = $('diagnostics')
  ul.innerHTML = ''
  if (!state.diagnostics.length) {
    const li = document.createElement('li')
    li.className = 'none'
    li.textContent = 'No diagnostics.'
    ul.appendChild(li)
    return
  }
  for (const d of state.diagnostics) {
    const li = document.createElement('li')
    li.innerHTML = `<span class="loc">${d.line}:${d.column}</span><span class="msg"></span>`
    li.querySelector('.msg').textContent = d.message
    li.addEventListener('click', () => {
      editor.setPosition({ lineNumber: d.line, column: d.column })
      editor.focus()
      editor.revealPositionInCenter({ lineNumber: d.line, column: d.column })
    })
    ul.appendChild(li)
  }
}

let checkTimer = null
let diagnosticEpoch = 0
editor.onDidChangeModelContent(() => { scheduleCheck(); invalidateAnalysis(); scheduleVocabulary() })
function scheduleCheck() {
  clearTimeout(checkTimer)
  checkTimer = setTimeout(async () => {
    if (projects && !projects.hasDocument()) return
    if (state.running || (activeProjectPath && !activeProjectPath.endsWith('.sos'))) return
    try {
      const version = editor.getModel().getVersionId()
      const epoch = diagnosticEpoch
      const r = await api('/api/check', { source: editor.getValue() })
      if (version === editor.getModel().getVersionId() && epoch === diagnosticEpoch) {
        setMarkers(r.diagnostics)
        renderCommands(r.commands || null)
      }
    } catch { /* transient */ }
  }, 350)
}

async function refreshCommands() {
  const model = editor.getModel()
  const version = model.getVersionId()
  const result = await api('/api/check', { source: model.getValue() })
  if (version !== model.getVersionId()) throw new Error('Source changed; try again')
  renderCommands(result.commands || null)
}

function setRunning(running, label) {
  state.running = running
  $('run').disabled = running || Boolean(activeProjectPath && !activeProjectPath.endsWith('.sos'))
  $('stop').disabled = !running
  const el = $('run-state')
  el.className = running ? 'is-running' : label || ''
  el.textContent = label || (running ? 'running…' : 'idle')
}

function showOutput(text, error) {
  $('output').textContent = [error ? `Error: ${error}` : '', text || (error ? '' : '(no output)')].filter(Boolean).join('\n\n')
}

function renderTraces(traces, usage) {
  const host = $('trace'); host.innerHTML = ''
  if (usage) {
    const summary = document.createElement('p'); summary.className = 'interp-usage'
    summary.textContent = usageLine(usage); host.appendChild(summary)
  }
  if (!traces?.length) { host.appendChild(document.createTextNode('No judgment traces.')); return }
  for (const t of traces) {
    const entry = document.createElement('article'); entry.className = 'trace-entry'
    const jump = document.createElement('button'); jump.className='trace-jump'; jump.textContent=`Line ${t.line}`
    jump.addEventListener('click',()=>{editor.revealLineInCenter(t.line);editor.setPosition({lineNumber:t.line,column:1});editor.focus()})
    const meta=document.createElement('span');meta.className='trace-meta';meta.textContent=`${t.model} · ${t.milliseconds} ms · ${t.inputTokens} tokens${t.replay?' · replay':''}`
    const question=document.createElement('p');question.textContent=t.question
    const answer=document.createElement('pre');answer.textContent=JSON.stringify(t.answer,null,2)
    entry.append(jump,meta)
    if (t.decision) {
      const decision=document.createElement('p'); decision.textContent=`${t.decision.toUpperCase()} — ${t.reason}`
      const item=document.createElement('pre'); item.textContent=JSON.stringify(t.item,null,2)
      entry.append(decision,item)
    }
    entry.append(question,answer);host.appendChild(entry)
  }
}

// ---- command selection ----

// Command scripts run one selected leaf: the picker is built from the
// declaration metadata the core returns with /api/check (never re-parsed in
// the browser), and input fields come from the declared inputs of that leaf
// plus its inherited options. Changing command or inputs invalidates any
// cached interpretation: it was resolved for a different selection.
const commandState = { tree: null, path: null, values: {} }



function renderCommands(tree) {
  const changedTree = JSON.stringify(tree) !== JSON.stringify(commandState.tree)
  if (!changedTree) return
  commandState.tree = tree
  const plain = $('plain-args'), bar = $('command-args')
  if (!tree) {
    bar.hidden = true
    plain.hidden = false
    commandState.path = null
    commandState.values = {}
    return
  }
  plain.hidden = true
  bar.hidden = false
  const leaves = commandLeaves(tree, [], [])
  const sel = $('command')
  sel.innerHTML = ''
  leaves.forEach((leaf, i) => {
    const o = document.createElement('option')
    o.value = String(i)
    o.textContent = leaf.path.join(' › ')
    sel.appendChild(o)
  })
  const previous = commandState.path
  const keep = previous && leaves.find((l) => l.path.join(' ') === previous)
  sel.value = keep ? String(leaves.indexOf(keep)) : '0'
  renderCommandInputs()
}

function renderCommandInputs() {
  const tree = commandState.tree
  const sel = $('command')
  const leaves = commandLeaves(tree, [], [])
  const leaf = leaves[Number(sel.value)] || leaves[0]
  if (!leaf) return
  const newPath = leaf.path.join(' ')
  if (newPath !== commandState.path) {
    commandState.path = newPath
    commandState.values = {}
    invalidateAnalysis()
  }
  $('command-desc').textContent = leaf.node.description || ''
  const host = $('command-inputs')
  host.innerHTML = ''
  // Ancestor commands for inherited inputs: every node above the leaf.
  const chain = []
  for (let i = 1; i < leaf.path.length; i++) chain.push(findByPath(tree, leaf.path.slice(0, i)))
  for (const input of effectiveInputs(leaf.node, chain)) {
    const field = document.createElement('span')
    field.className = 'cmd-field'
    const label = document.createElement('label')
    label.textContent = input.name
    if (input.required) { const req = document.createElement('span'); req.className = 'req'; req.textContent = ' *'; label.appendChild(req) }
    label.title = `${input.kind} as ${input.type}` + (input.description ? ` — ${input.description}` : '')
    field.appendChild(label)
    field.appendChild(commandInputControl(input))
    host.appendChild(field)
  }
  if (!host.childElementCount) {
    const none = document.createElement('span')
    none.className = 'cmd-desc'
    none.textContent = 'no declared inputs'
    host.appendChild(none)
  }
}


function commandInputControl(input) {
  const key = commandState.path + '/' + input.name
  const stored = () => commandState.values[key]
  let control
  if (input.choices?.length) {
    control = document.createElement('select')
    control.className = 'select'
    for (const choice of input.choices) {
      const o = document.createElement('option')
      o.value = choice
      o.textContent = choice
      control.appendChild(o)
    }
    if (!input.default) {
      const prompt = document.createElement('option')
      prompt.value = ''
      prompt.textContent = 'Choose…'
      control.prepend(prompt)
    }
    control.value = stored() ?? input.default ?? ''
    control.addEventListener('change', () => { commandState.values[key] = control.value; invalidateAnalysis() })
  } else if (input.kind === 'switch') {
    control = document.createElement('input')
    control.type = 'checkbox'
    control.checked = stored() ?? (input.default === 'true' || input.default === 'on')
    control.addEventListener('change', () => { commandState.values[key] = control.checked; invalidateAnalysis() })
  } else {
    control = document.createElement('input')
    control.className = 'input'
    control.spellcheck = false
    control.placeholder = input.default ?? ''
    control.value = stored() ?? ''
    control.addEventListener('input', () => { commandState.values[key] = control.value; invalidateAnalysis() })
  }
  control.setAttribute('aria-label', input.name)
  return control
}

// Selected command path (array of names) and declared input values. Empty
// value fields are omitted so defaults and required-input errors come from
// the core, not from a second opinion in the browser.
function commandSelection() {
  if (!commandState.tree || !commandState.path) return null
  const leaves = commandLeaves(commandState.tree, [], [])
  const leaf = leaves.find((l) => l.path.join(' ') === commandState.path)
  if (!leaf) return null
  const args = {}
  const chain = []
  for (let i = 1; i < leaf.path.length; i++) chain.push(findByPath(commandState.tree, leaf.path.slice(0, i)))
  for (const input of effectiveInputs(leaf.node, chain)) {
    const raw = commandState.values[commandState.path + '/' + input.name]
    if (input.kind === 'switch') { if (raw !== undefined) args[input.name] = Boolean(raw); continue }
    if (typeof raw === 'string' && raw.trim() === '') continue
    if (raw !== undefined) args[input.name] = raw
  }
  // Options.CommandPath carries child names only; the root is implicit.
  return { commandPath: leaf.path.slice(1), args }
}

$('arguments').addEventListener('input', invalidateAnalysis)

document.addEventListener('change', (e) => {
  if (e.target.id === 'command') { commandState.values = {}; renderCommandInputs() }
})

// ---- on-demand interpretation ----

// A stale analysis must never speak for a newer document: any edit aborts the
// in-flight request (the server may still finish and spend budget; its result
// is discarded) and marks what is on screen as stale.
function invalidateAnalysis() {
  if (state.analyzing && state.analysisAbort) state.analysisAbort.abort()
  if (state.analysis) {
    state.analysis = null
    markInterpretationStale()
  }
}

function markInterpretationStale() {
  const host = $('interpretation')
  if (!host.childElementCount || host.classList.contains('is-stale')) return
  host.classList.add('is-stale')
  const note = document.createElement('p')
  note.className = 'interp-stale'
  note.textContent = 'source or command inputs changed — analyze again'
  host.prepend(note)
}

function setAnalyzing(analyzing) {
  state.analyzing = analyzing
  $('analyze').disabled = analyzing
  $('analyze').textContent = analyzing ? 'Analyzing…' : 'Analyze'
}

async function runAnalyze() {
  if (projects && !projects.hasDocument()) return
  if (state.analyzing) return
  try { await refreshCommands() } catch (e) { renderInterpretationError(e); return }
  if (state.analyzing) return
  document.querySelector('.tab[data-tab="interpretation"]')?.click()
  const model = editor.getModel()
  const version = model.getVersionId()
  const abort = new AbortController()
  state.analysisAbort = abort
  setAnalyzing(true)
  const host = $('interpretation')
  host.classList.remove('is-stale')
  host.textContent = 'analyzing…'
  try {
    const originalSource = model.getValue()
    const selection = commandSelection()
    const r = await api('/api/analyze', {
      source: originalSource,
      commandPath: selection ? selection.commandPath : [],
      args: selection ? selection.args : {}
    }, 'POST', abort.signal)
    r.originalSource = originalSource
    if (abort.signal.aborted) return
    if (model.getVersionId() !== version) { markInterpretationStale(); return }
    diagnosticEpoch++
    setMarkers(r.analysis?.diagnostics || [])
    state.analysis = { version, result: r }
    renderInterpretation(r, version)
  } catch (e) {
    if (e.name === 'AbortError') return
    renderInterpretationError(e)
  } finally {
    if (state.analysisAbort === abort) state.analysisAbort = null
    setAnalyzing(false)
  }
}

function renderInterpretationError(e) {
  const host = $('interpretation')
  host.classList.remove('is-stale')
  host.innerHTML = ''
  const el = document.createElement('p')
  el.className = 'interp-error'
  el.textContent = e.kind ? `${e.kind}: ${e.message}` : (e.message || 'analysis failed')
  host.appendChild(el)
  if (e.analysis) {
    const u = document.createElement('p')
    u.className = 'interp-usage'
    u.textContent = usageLine(e.analysis.usage)
    host.appendChild(u)
    for (const d of e.analysis.diagnostics || []) host.appendChild(interpDiagnostic(d))
  }
}

function renderInterpretation(result, version) {
  const host = $('interpretation')
  host.classList.remove('is-stale')
  host.innerHTML = ''
  const a = result.analysis || {}
  const meta = document.createElement('p')
  meta.className = 'interp-meta'
  const bits = []
  if (a.model) bits.push(`model ${a.model}`)
  if (result.promoted) bits.push('canonical policy promoted to assisted — entries are suggestions')
  if (result.reused) bits.push('cache reuse · no new resolution')
  meta.textContent = bits.join(' · ') || 'analysis complete'
  host.appendChild(meta)
  const usage = usageLine(a.usage)
  if (usage) {
    const u = document.createElement('p')
    u.className = 'interp-usage'
    u.textContent = usage
    host.appendChild(u)
  }
  const decisions = a.decisions || []
  if (!decisions.length) {
    const none = document.createElement('p')
    none.className = 'interp-none'
    none.textContent = 'No interpretation decisions.'
    host.appendChild(none)
  }
  const originalLines = (result.originalSource || '').split(/\r?\n/)
  for (const d of decisions) host.appendChild(interpEntry(d, version, originalLines[d.line - 1]))
  for (const d of a.diagnostics || []) host.appendChild(interpDiagnostic(d))
}


function interpEntry(d, version, originalLine) {
  const entry = document.createElement('article')
  entry.className = 'interp-entry'
  const head = document.createElement('div')
  head.className = 'interp-head'
  const jump = document.createElement('button')
  jump.className = 'trace-jump'
  jump.textContent = `Line ${d.line}`
  jump.addEventListener('click', () => {
    editor.revealLineInCenter(d.line)
    editor.setPosition({ lineNumber: d.line, column: 1 })
    editor.focus()
  })
  const method = document.createElement('span')
  method.className = 'interp-method'
  method.textContent = d.method || 'unresolved'
  head.append(jump, method)
  if (typeof d.confidence === 'number') {
    const conf = document.createElement('span')
    conf.className = 'interp-conf'
    conf.textContent = `${Math.round(d.confidence * 100)}%`
    head.appendChild(conf)
  } else if (d.confidence) {
    const conf = document.createElement('span')
    conf.className = 'interp-conf'
    conf.textContent = String(d.confidence)
    head.appendChild(conf)
  }
  entry.appendChild(head)
  if (d.source) {
    const src = document.createElement('p')
    src.className = 'interp-src'
    src.textContent = d.source
    entry.appendChild(src)
  }
  if (d.canonical && d.canonical.trim() && d.method !== 'criterion' && d.canonical !== d.source) {
    const canon = document.createElement('p')
    canon.className = 'interp-canon'
    canon.textContent = d.canonical
    entry.appendChild(canon)
    const apply = document.createElement('button')
    apply.className = 'interp-apply'
    apply.textContent = 'Apply precise form'
    apply.addEventListener('click', () => applyInterpretation(d, version, originalLine))
    entry.appendChild(apply)
  }
  if (d.explanation) {
    const ex = document.createElement('p')
    ex.className = 'interp-explain'
    ex.textContent = d.explanation
    entry.appendChild(ex)
  }
  return entry
}

function interpDiagnostic(d) {
  const el = document.createElement('p')
  el.className = 'interp-diag'
  const loc = document.createElement('button')
  loc.className = 'interp-diag-loc'
  loc.textContent = `${d.line}:${d.column}`
  loc.addEventListener('click', () => {
    editor.revealLineInCenter(d.line)
    editor.setPosition({ lineNumber: d.line, column: d.column })
    editor.focus()
  })
  el.append(loc, ` ${d.message}`)
  return el
}

// Applying a precise form is never automatic: the document version must still
// be the analyzed one and the line must still carry the analyzed source.
function applyInterpretation(d, version, originalLine) {
  const model = editor.getModel()
  if (!model) return
  if (model.getVersionId() !== version) {
    renderInterpretationError(new Error('document changed since analysis — analyze again'))
    return
  }
  const line = d.line
  if (!Number.isInteger(line) || line < 1 || line > model.getLineCount()) {
    renderInterpretationError(new Error('line no longer exists — analyze again'))
    return
  }
  const current = model.getLineContent(line)
  if (typeof originalLine !== 'string' || current !== originalLine) {
    renderInterpretationError(new Error('source line changed since analysis — analyze again'))
    return
  }
  // Keep the author's trailing comment when replacing a sentence.
  let quoted = false, escaped = false, comment = ''
  for (let i = 0; i < current.length; i++) {
    const ch = current[i]
    if (escaped) { escaped = false; continue }
    if (ch === '\\' && quoted) { escaped = true; continue }
    if (ch === '"') quoted = !quoted
    if (ch === '#' && !quoted) { comment = current.slice(i); break }
  }
  const precise = d.canonical.split('\n')
  if (comment) precise[0] += ` ${comment}`
  model.pushEditOperations([], [{
    range: { startLineNumber: line, startColumn: 1, endLineNumber: line, endColumn: current.length + 1 },
    text: precise.join('\n')
  }], () => null)
}

// ---- vocabulary dictionary ----

// One catalog per source context, fetched from the shared sos/vocabulary
// method through the LSP bridge. Search is local (typing never round-trips);
// source edits mark the catalog dirty and refresh is debounced while the
// tab is open. A stale response never renders: each request carries a
// sequence number and aborts its predecessor.
const vocab = { seq: 0, timer: null, abort: null, view: 'enabled', data: null, error: null, loading: false, dirty: true, library: '' }

const vocabTabButton = () => document.querySelector('.tab[data-tab="vocabulary"]')
const vocabPanelActive = () => $('panel-vocabulary').classList.contains('is-active')

function scheduleVocabulary() {
  vocab.dirty = true
  if (!vocabPanelActive()) return
  clearTimeout(vocab.timer)
  vocab.timer = setTimeout(refreshVocabulary, 350)
}

async function refreshVocabulary() {
  clearTimeout(vocab.timer)
  const seq = ++vocab.seq
  const abort = new AbortController()
  if (vocab.abort) vocab.abort.abort()
  vocab.abort = abort
  vocab.dirty = false
  vocab.loading = true
  vocab.error = null
  renderVocabulary()
  try {
    const r = await api('/api/lsp', { source: editor.getValue(), method: 'sos/vocabulary' }, 'POST', abort.signal)
    if (seq !== vocab.seq) return
    vocab.data = normalizeCatalog(r && r.result)
    if (!vocab.data) vocab.error = new Error('unrecognized vocabulary catalog')
  } catch (e) {
    if (seq !== vocab.seq || e.name === 'AbortError') return
    vocab.error = e
  } finally {
    if (seq === vocab.seq) {
      vocab.loading = false
      vocab.abort = null
      renderVocabulary()
    }
  }
}

function renderVocabulary() {
  const body = $('vocab-body')
  const conflicts = $('vocab-conflicts')
  body.innerHTML = ''
  conflicts.innerHTML = ''
  body.setAttribute('aria-busy', vocab.loading ? 'true' : 'false')
  if (vocab.loading && !vocab.data) {
    const note = document.createElement('p')
    note.className = 'vocab-note'
    note.textContent = 'loading vocabulary…'
    body.appendChild(note)
    return
  }
  if (vocab.error) {
    const el = document.createElement('p')
    el.className = 'vocab-error'
    el.textContent = `vocabulary unavailable: ${vocab.error.message || 'request failed'}`
    body.appendChild(el)
    const retry = document.createElement('button')
    retry.className = 'interp-apply'
    retry.type = 'button'
    retry.textContent = 'Retry'
    retry.addEventListener('click', refreshVocabulary)
    body.appendChild(retry)
    return
  }
  if (!vocab.data) return
  renderVocabDiagnostics(conflicts)
  const query = $('vocab-search').value
  if (vocab.view === 'enabled') renderVocabEnabled(body, query)
  else renderVocabExplorer(body, query)
}

function renderVocabDiagnostics(host) {
  const list = diagnosticsFor(vocab.data.diagnostics, vocab.library)
  if (!list.length) return
  const box = document.createElement('div')
  box.className = 'vocab-conflict'
  const title = document.createElement('p')
  title.className = 'vocab-conflict-title'
  title.textContent = 'Vocabulary problems — check/build/lint report these deterministically'
  box.appendChild(title)
  for (const d of list) {
    const row = document.createElement('p')
    row.className = 'vocab-conflict-row'
    if (Number.isInteger(d.line) && d.line >= 1) {
      const loc = document.createElement('button')
      loc.className = 'vocab-declared'
      loc.type = 'button'
      loc.textContent = `${d.line}:${d.column || 1}`
      loc.addEventListener('click', () => {
        editor.revealLineInCenter(d.line)
        editor.setPosition({ lineNumber: d.line, column: d.column || 1 })
        editor.focus()
      })
      row.appendChild(loc)
      row.append(' ')
    }
    row.append(d.message)
    box.appendChild(row)
  }
  host.appendChild(box)
}

// Enabled here: entries whose vocabulary the current source + config
// actually enable, grouped by their libraries with provenance.
function renderVocabEnabled(body, query) {
  const view = enabledView(vocab.data)
  const entries = searchEntries(view.entries, query)
  const byPath = new Map()
  for (const e of entries) {
    if (!byPath.has(e.library)) byPath.set(e.library, [])
    byPath.get(e.library).push(e)
  }
  const headerMatch = new Set(searchLibraries(view.libraries, query).map((l) => l.path))
  const rows = view.libraries.filter((l) =>
    (!vocab.library || l.path === vocab.library) &&
    (!query.trim() || headerMatch.has(l.path) || byPath.has(l.path)))
  if (!rows.length) { body.appendChild(vocabEmpty(query)); return }
  for (const lib of rows) {
    body.appendChild(vocabLibraryHeader(lib, byPath.get(lib.path) || []))
    for (const e of byPath.get(lib.path) || []) body.appendChild(vocabEntry(e))
  }
}

// Library explorer: every catalog library with an import preview of what
// each import form enables, per the agreed semantics.
function renderVocabExplorer(body, query) {
  const scoped = vocab.library
    ? { ...vocab.data, libraries: vocab.data.libraries.filter((l) => l.path === vocab.library) }
    : vocab.data
  const groups = libraryView(scoped, query)
  if (!groups.length) { body.appendChild(vocabEmpty(query)); return }
  for (const group of groups) {
    const lib = group.library
    const live = libraryEnabled(lib)
    const card = document.createElement('article')
    card.className = 'vocab-lib-card' + (live ? ' is-enabled' : '')
    card.appendChild(vocabLibraryHeader(lib, group.entries))
    const preview = document.createElement('p')
    preview.className = 'vocab-preview'
    const forms = importPreview(lib, group.sampleWords)
    preview.textContent = `${forms.open}\n${forms.aliased}`
    if (live) preview.append(`\nenabled here — ${lib.bare ? `bare vocabulary active (${lib.alias} qualified calls also valid)` : `${lib.alias}. prefix required`}`)
    else if (lib.error) preview.append(`\nresolution problem: ${lib.error}`)
    card.appendChild(preview)
    if (group.entries.length) {
      for (const e of group.entries) {
        const entry = vocabEntry(e)
        if (!live && e.enabled !== true) {
          const hint = document.createElement('span')
          hint.className = 'vocab-chip is-muted'
          hint.textContent = e.import ? 'import to enable' : 'not enabled here'
          entry.querySelector('.vocab-entry-head').appendChild(hint)
        }
        card.appendChild(entry)
      }
    } else {
      const none = document.createElement('p')
      none.className = 'vocab-empty'
      none.textContent = 'no registered vocabulary entries'
      card.appendChild(none)
    }
    body.appendChild(card)
  }
}

function vocabEmpty(query) {
  const el = document.createElement('p')
  el.className = 'vocab-empty'
  if (query.trim()) {
    el.textContent = `No matches for “${query.trim()}”.`
    const clear = document.createElement('button')
    clear.className = 'interp-apply'
    clear.type = 'button'
    clear.style.marginLeft = '8px'
    clear.textContent = 'Clear search'
    clear.addEventListener('click', () => { $('vocab-search').value = ''; renderVocabulary() })
    el.appendChild(clear)
  } else if (!vocab.data.libraries.length) {
    el.textContent = 'No libraries found for this workspace.'
  } else {
    el.textContent = 'Nothing enabled here yet — import a library or configure language.libraries.'
  }
  return el
}

function vocabOriginClass(origin) {
  const value = String(origin || '')
  if (value === 'import') return 'is-origin-imported'
  if (value.startsWith('config')) return 'is-origin-configured'
  if (value === 'standard library') return 'is-origin-builtin'
  if (value.startsWith('local')) return 'is-origin-local'
  return 'is-origin-unknown'
}

function vocabLibraryHeader(lib, ownEntries) {
  const head = document.createElement('div')
  head.className = 'vocab-lib'
  const path = document.createElement('span')
  path.className = 'vocab-lib-path'
  path.textContent = lib.path
  head.appendChild(path)
  const originLabel = String(lib.origin || '').startsWith('config ') ? 'Configured' : lib.origin === 'import' ? 'Imported here' : String(lib.origin || '').startsWith('local ') ? 'Local library' : lib.origin || 'Unknown origin'
  head.appendChild(vocabChip(originLabel, vocabOriginClass(lib.origin)))
  if (lib.bare) head.appendChild(vocabChip(`bare words + ${lib.alias}. qualified`, ''))
  else if (lib.alias) head.appendChild(vocabChip(`requires ${lib.alias}. prefix`, 'is-alias'))
  const detail = document.createElement('p')
  detail.className = 'vocab-lib-detail'
  const count = ownEntries.length ? ` · ${ownEntries.length} word${ownEntries.length === 1 ? '' : 's'}` : ''
  detail.textContent = `${lib.origin || 'unknown origin'}${count}`
  head.appendChild(detail)
  return head
}

function vocabChip(text, className) {
  const chip = document.createElement('span')
  chip.className = 'vocab-chip' + (className ? ' ' + className : '')
  chip.textContent = text
  return chip
}

function vocabEntry(entry) {
  const el = document.createElement('article')
  el.className = 'vocab-action'
  const head = document.createElement('div')
  head.className = 'vocab-entry-head'
  const name = document.createElement('span')
  name.className = 'vocab-action-name'
  name.textContent = entry.name
  head.appendChild(name)
  const sig = document.createElement('span')
  sig.className = 'vocab-sig'
  sig.textContent = entrySignature(entry)
  head.appendChild(sig)
  if (needsJev(entry)) {
    const jev = document.createElement('span')
    jev.className = 'vocab-jev'
    jev.textContent = 'needs provider (Jev)'
    head.appendChild(jev)
  }
  el.appendChild(head)
  const patterns = (Array.isArray(entry.patterns) ? entry.patterns : []).filter(Boolean)
  if (patterns.length) {
    const forms = document.createElement('p')
    forms.className = 'vocab-example'
    forms.textContent = patterns.join('\n')
    el.appendChild(forms)
  }
  if (entry.description) {
    const desc = document.createElement('p')
    desc.className = 'vocab-desc'
    desc.textContent = entry.description
    el.appendChild(desc)
  }
  const effects = (Array.isArray(entry.effects) ? entry.effects : []).filter(Boolean)
  if (effects.length) {
    const row = document.createElement('p')
    row.className = 'vocab-syn'
    row.textContent = `effects: ${effects.join(', ')}`
    el.appendChild(row)
  } else {
    // Unknown is not "no effects": the catalog not proving effects never
    // reads as a Jev-free guarantee.
    const row = document.createElement('p')
    row.className = 'vocab-syn'
    row.textContent = 'effects: unknown'
    el.appendChild(row)
  }
  const synonyms = (Array.isArray(entry.synonyms) ? entry.synonyms : []).filter(Boolean)
  if (synonyms.length) {
    const row = document.createElement('p')
    row.className = 'vocab-syn'
    row.textContent = `also matches: ${synonyms.join(', ')}`
    el.appendChild(row)
  }
  return el
}

for (const button of [$('vocab-view-enabled'), $('vocab-view-library')]) {
  button.addEventListener('click', () => {
    vocab.view = button.id === 'vocab-view-enabled' ? 'enabled' : 'library'
    for (const b of [$('vocab-view-enabled'), $('vocab-view-library')]) {
      b.classList.toggle('is-active', b === button)
      b.setAttribute('aria-pressed', String(b === button))
    }
    renderVocabulary()
  })
}

let vocabSearchTimer = null
$('vocab-search').addEventListener('input', () => {
  clearTimeout(vocabSearchTimer)
  vocabSearchTimer = setTimeout(renderVocabulary, 120)
})

function renderVocabLibraryChip() {
  const chip = $('vocab-library-clear')
  if (!vocab.library) { chip.hidden = true; return }
  chip.hidden = false
  chip.textContent = `library: ${vocab.library} ✕`
  chip.setAttribute('aria-label', `Scoped to library ${vocab.library}; activate to clear`)
}
$('vocab-library-clear').addEventListener('click', () => {
  vocab.library = ''
  renderVocabLibraryChip()
  renderVocabulary()
})

vocabTabButton()?.addEventListener('click', () => {
  renderVocabLibraryChip()
  if ((vocab.dirty || !vocab.data) && !vocab.loading) refreshVocabulary()
})

// Right-clicking an import line (or any word) opens the dictionary scoped
// to that library or searched for that word.
editor.addAction({
  id: 'sos.openVocabulary',
  label: 'Open Vocabulary for Library at Cursor',
  contextMenuGroupId: 'navigation',
  contextMenuOrder: 1,
  run() {
    const position = editor.getPosition()
    const model = editor.getModel()
    if (!model || !position) return
    const line = model.getLineContent(position.lineNumber)
    const imported = line.match(/^\s*import\s+"([^"]+)"/)
    if (imported) openVocabularyFor(imported[1])
    else {
      const word = model.getWordAtPosition(position)
      openVocabularyFor(null, word ? word.word : '')
    }
  }
})

function openVocabularyFor(library, query = '') {
  vocabTabButton()?.click()
  vocab.library = library || ''
  if (query) $('vocab-search').value = query
  renderVocabLibraryChip()
  if ((vocab.dirty || !vocab.data) && !vocab.loading) refreshVocabulary()
  else renderVocabulary()
}

// Import lines announce the dictionary in hover; Monaco merges this with
// the language server's own hover contents.
monaco.languages.registerHoverProvider('sos', {
  provideHover(model, position) {
    const line = model.getLineContent(position.lineNumber)
    const imported = line.match(/^\s*import\s+"([^"]+)"/)
    if (!imported) return null
    return { contents: [{ value: `---\n**Vocabulary:** dictionary for \`${imported[1]}\` — right-click → *Open Vocabulary for Library at Cursor*.` }] }
  }
})

$('run').addEventListener('click', async () => {
  if (projects && !projects.hasDocument()) return
  if (state.running) return
  if (activeProjectPath && !activeProjectPath.endsWith('.sos')) { showOutput('', 'Select a .sos file to run.'); return }
  if (projects?.hasUnsavedImports()) { showOutput('', 'Save other edited project files before running; imports are loaded from disk.'); return }
  const epoch = documentEpoch
  setRunning(true)
  const started = performance.now()
  const tick = setInterval(() => {
    if (epoch !== documentEpoch) return
    $('run-state').textContent = `running… ${((performance.now() - started) / 1000).toFixed(1)}s`
  }, 100)
  try {
    await refreshCommands()
    if (epoch !== documentEpoch) { clearInterval(tick); setRunning(false); scheduleCheck(); return }
    const selection = commandSelection()
    const r = await api('/api/run', {
      source: editor.getValue(),
      commandPath: selection ? selection.commandPath : [],
      args: selection ? selection.args : JSON.parse($('arguments').value || '{}'),
      timeoutMs: state.session ? state.session.defaultTimeoutMs : 30000
    })
    clearInterval(tick)
    if (epoch !== documentEpoch) { setRunning(false); scheduleCheck(); return }
    const parts = []
    if (r.output) parts.push(r.output)
    if (r.stderr) parts.push(r.stderr)
    showOutput(parts.join('\n') || (r.ok ? `(no output, ${r.steps} steps)` : ''), r.ok ? null : r.error ? `${r.error.kind}: ${r.error.message}` : 'run failed')
    renderTraces(r.traces, r.usage)
    diagnosticEpoch++
    if (r.analysis) {
      renderInterpretation({analysis:r.analysis, originalSource:editor.getValue()}, editor.getModel().getVersionId())
    }
    if (r.ok && r.traces?.length) {
      const summary = document.createElement('p')
      const kept = r.traces.filter(t => t.decision === 'keep').length
      const discarded = r.traces.filter(t => t.decision === 'discard').length
      summary.textContent = `Jev completed ${r.traces.length} judgments${kept+discarded ? `: ${kept} kept, ${discarded} discarded` : ''}.`
      const details = document.createElement('button'); details.textContent='See decisions and tickets'
      details.addEventListener('click',()=>document.querySelector('[data-tab="trace"]').click())
      const interpretation = document.createElement('button'); interpretation.textContent='See sentence interpretations'
      interpretation.addEventListener('click',()=>document.querySelector('[data-tab="interpretation"]').click())
      $('output').append(summary,details,interpretation)
    }
    if (r.diagnostics) setMarkers(r.diagnostics)
    setRunning(false, r.ok ? 'ok' : 'error')
    $('run-state').textContent = r.ok ? `ok · ${r.steps} steps · ${Math.round(r.durationMs)} ms` : 'failed'
  } catch (e) {
    clearInterval(tick)
    if (epoch !== documentEpoch) { setRunning(false); scheduleCheck(); return }
    showOutput('', e.kind ? `${e.kind}: ${e.message}` : e.message)
    setRunning(false, 'error')
  }
})

$('stop').addEventListener('click', async () => {
  try { await api('/api/cancel', {}) } catch { /* already finished */ }
})

$('analyze').addEventListener('click', runAnalyze)

$('format').addEventListener('click', async () => {
  try {
    const r = await api('/api/format', { source: editor.getValue() })
    const pos = editor.getPosition()
    if (!(r.diagnostics || []).length) editor.setValue(r.source)
    if (pos) editor.setPosition(pos)
    setMarkers(r.diagnostics)
  } catch (e) { showOutput('', e.message) }
})

$('save').addEventListener('click', async () => {
  if (projects?.isProject()) { try { await projects.save() } catch(e) { showOutput('', e.message) }; return }
  const name = $('filename').value.trim() || 'script.sos'
  try {
    if (window.go?.main?.Desktop?.Save) { await window.go.main.Desktop.Save(name, editor.getValue()); return }
    const res = await fetch('/api/save', {
      method: 'POST',
      headers: { 'X-Studio-Token': TOKEN, 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, source: editor.getValue() })
    })
    if (!res.ok) {
      const data = await res.json().catch(() => ({}))
      throw new Error((data.error && data.error.message) || res.statusText)
    }
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.click()
    URL.revokeObjectURL(url)
  } catch (e) { showOutput('', 'save failed: ' + e.message) }
})

for (const tab of document.querySelectorAll('.tab')) {
  tab.addEventListener('click', () => {
    document.querySelectorAll('.tab').forEach((t) => {t.classList.toggle('is-active', t === tab);t.setAttribute('aria-selected',String(t===tab))})
    document.querySelectorAll('.panel').forEach((p) =>
      p.classList.toggle('is-active', p.id === `panel-${tab.dataset.tab}`))
  })
}

window.addEventListener('keydown', (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') { e.preventDefault(); $('run').click() }
  if ((e.metaKey || e.ctrlKey) && e.key === 's') { e.preventDefault(); $('save').click() }
})

// Loading a different document creates a new panel context. Late responses
// from the old document must not repopulate its results.
let documentEpoch = 0
let openSequence = 0
function loadDocument(source, name) {
  documentEpoch++
  diagnosticEpoch++
  clearTimeout(checkTimer)
  state.analysisAbort?.abort()
  state.analysis = null
  $('interpretation').classList.remove('is-stale')
  $('interpretation').textContent = 'Analyze this script to inspect its meaning.'
  showOutput('Run this script to see output.')
  renderTraces([])
  setMarkers([])
  commandState.values = {}
  renderCommands(null)
  $('arguments').value = '{}'
  clearTimeout(vocab.timer)
  vocab.seq++
  vocab.abort?.abort()
  vocab.data = null; vocab.error = null; vocab.loading = false; vocab.dirty = true; vocab.library = ''
  $('vocab-search').value = ''
  editor.setValue(source)
  $('filename').value = name
  scheduleCheck()
  refreshVocabulary()
}

async function init() {
  try {
    state.session = await api('/api/session', undefined, 'GET')
    $('workdir').textContent = state.session.dir
    $('version').textContent = 'sos ' + state.session.version
    const ex = await api('/api/examples', undefined, 'GET')
    const sel = $('examples')
    sel.innerHTML = ''
    const blank = document.createElement('option')
    blank.value = ''
    blank.textContent = '(blank)'
    sel.appendChild(blank)
    for (const item of ex.examples) {
      const o = document.createElement('option')
      o.value = item.name
      o.textContent = item.title
      sel.appendChild(o)
    }
    sel.addEventListener('change', async () => {
      const sequence = ++openSequence
      const name = sel.value
      if (!name) { loadDocument('', 'script.sos'); return }
      try {
        const r = await api('/api/open', { name })
        if (sequence !== openSequence) return
        loadDocument(r.source, name + '.sos')
      } catch (e) { showOutput('', e.message) }
    })
    if (ex.examples.length) {
      const initial = ex.examples.find(item => item.name === 'jev-workflow') || ex.examples[0]
      const r = await api('/api/open', { name: initial.name })
      loadDocument(r.source, initial.name + '.sos'); sel.value = initial.name
    }
  } catch (e) {
    showOutput('', 'session failed: ' + e.message)
  }
  await projects.init()
  scheduleCheck()
  if (projects.hasDocument()) editor.focus()
}

projects = installProjects({api, editor, monaco, busy:()=>state.running||state.analyzing, load:loadDocument, onPath:path=>{openSequence++;activeProjectPath=path; const textOnly=Boolean(path&&!path.endsWith('.sos')); $('run').disabled=state.running||textOnly; $('analyze').disabled=textOnly; $('format').disabled=textOnly}, report:e=>showOutput('',e.message)})
init()

$('open').addEventListener('click', async () => {
  try {
    if (window.go?.main?.Desktop?.Open) {
      const file = await window.go.main.Desktop.Open()
      if (file.name) { loadDocument(file.source, file.name) }
    } else { $('file-input').click() }
  } catch (e) { showOutput('', String(e)) }
})
$('file-input').addEventListener('change', async e => {
  const file = e.target.files[0]
  if (file) { loadDocument(await file.text(), file.name) }
})

if (window.go?.main?.Desktop?.ChooseFolder) {
  $('choose-folder').hidden = false
  $('choose-folder').addEventListener('click', async () => {
    try { await projects.changeRoot() }
    catch (e) { showOutput('', String(e)) }
  })
}

monaco.editor.registerCommand("sos.analyze", () => runAnalyze())
