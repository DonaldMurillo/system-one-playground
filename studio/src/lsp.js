// Monaco adapters for the shared offline SOS language server.
import { usageCost } from './usage.js'
export const SEMANTIC_LEGEND = {
  tokenTypes: ['keyword', 'variable', 'parameter', 'function', 'type', 'namespace', 'string', 'number', 'comment', 'macro', 'enumMember', 'operator'],
  tokenModifiers: []
}
export const toEditorRange = r => ({startLineNumber:r.start.line+1,startColumn:r.start.character+1,endLineNumber:r.end.line+1,endColumn:r.end.character+1})
const toLSPRange = r => ({start:{line:r.startLineNumber-1,character:r.startColumn-1},end:{line:r.endLineNumber-1,character:r.endColumn-1}})

export function installLanguageServices(monaco, api, context = () => ({}), analysis = () => null) {
  const registrations = []
  const codeLensListeners = new Set()
  const codeLensEvent = listener => {
    codeLensListeners.add(listener)
    return {dispose(){ codeLensListeners.delete(listener) }}
  }
  const request = async (model, method, params = {}, token) => {
    const version = model.getVersionId()
    if (token?.isCancellationRequested || model.isDisposed()) return null
    const controller = new AbortController()
    const cancel = token?.onCancellationRequested?.(() => controller.abort())
    let response
    try { response = await api('/api/lsp', {source:model.getValue(), method, ...context(), ...params}, 'POST', controller.signal) }
    finally { cancel?.dispose() }
    // Never apply edits/hints belonging to a document snapshot the user has changed.
    if (token?.isCancellationRequested || model.isDisposed() || model.getVersionId() !== version) return null
    return response.result
  }
  if (monaco.languages.registerCodeLensProvider) registrations.push(monaco.languages.registerCodeLensProvider('sos', {
    onDidChange: codeLensEvent,
    provideCodeLenses: async (model, token) => {
      try {
        const items = await request(model, 'textDocument/codeLens', {}, token)
        const decisions = new Map((analysis()?.decisions || []).map(decision => [decision.line, decision]))
        return {lenses:(items || []).map(item=>{
          const range = toEditorRange(item.range)
          const decision = decisions.get(range.startLineNumber)
          let title = item.command.title
          if (decision?.method === 'jev') {
            const confidence = `${Math.round(decision.confidence * 100)}% confidence`
            const usage = decision.usage_known
              ? `${decision.input_tokens} tokens · ~${usageCost(decision.input_tokens)}${decision.usage_shared ? ` shared across ${decision.batch_size} lines` : ''}`
              : 'usage unavailable'
            title = `Jev · ${confidence} · ${usage}`
          } else if (decision?.method === 'deterministic') title = 'Deterministic · no Jev cost'
          return {range,command:{id:item.command.command,title}}
        }),dispose(){}}
      } catch { return {lenses:[],dispose(){}} }
    }
  }))
  const at = position => ({position:{line:position.lineNumber-1,character:position.column-1}})
  const edits = values => (values || []).map(e => ({range:toEditorRange(e.range),text:e.newText}))
  const safe = fn => async (...args) => { try { return await fn(...args) } catch { return null } }
  registrations.push(monaco.languages.registerCompletionItemProvider('sos', {
    triggerCharacters: ['.'],
    provideCompletionItems: safe(async (model, position, _context, token) => {
      const word = model.getWordUntilPosition(position)
      const result = await request(model,'textDocument/completion',at(position),token)
      const items = Array.isArray(result) ? result : result?.items || []
      return {suggestions:items.map(item=>({
        label:item.label, insertText:item.textEdit?.newText || item.insertText || item.label,
        kind:completionKind(monaco, item.kind), detail:item.detail,
        documentation:typeof item.documentation === 'object' ? {value:item.documentation.value} : item.documentation,
        sortText:item.sortText, filterText:item.filterText,
        insertTextRules:item.insertTextFormat === 2 ? monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet : undefined,
        additionalTextEdits:edits(item.additionalTextEdits),
        range:item.textEdit?.range ? toEditorRange(item.textEdit.range) : {startLineNumber:position.lineNumber,endLineNumber:position.lineNumber,startColumn:word.startColumn,endColumn:word.endColumn}
      }))}
    })
  }))
  registrations.push(monaco.languages.registerHoverProvider('sos', {
    provideHover: safe(async (model, position, token) => {
      const result = await request(model,'textDocument/hover',at(position),token)
      if (!result) return null
      const contents=Array.isArray(result.contents)?result.contents:[result.contents]
      return {contents:contents.map(v=>({value:typeof v==='string'?v:v.value})),range:result.range&&toEditorRange(result.range)}
    })
  }))
  registrations.push(monaco.languages.registerDefinitionProvider('sos', {
    provideDefinition: safe(async (model,position,token) => {
      const r=await request(model,'textDocument/definition',at(position),token)
      if (!r) return null
      // Studio edits a single buffer; never misdirect a cross-file definition to it.
      if (r.uri && !r.uri.endsWith('/buffer.sos') && r.uri !== model.uri.toString()) return null
      return {uri:model.uri,range:toEditorRange(r.range)}
    })
  }))
  registrations.push(monaco.languages.registerDocumentFormattingEditProvider('sos', {
    provideDocumentFormattingEdits: safe(async (model,_options,token) => edits(await request(model,'textDocument/formatting',{},token)))
  }))
  registrations.push(monaco.languages.registerDocumentSemanticTokensProvider('sos', {
    getLegend: () => SEMANTIC_LEGEND,
    provideDocumentSemanticTokens: safe(async (model,_lastResultId,token) => {
      const r=await request(model,'textDocument/semanticTokens/full',{},token)
      return r ? {data:new Uint32Array(r.data),resultId:r.resultId} : null
    }),
    releaseDocumentSemanticTokens() {}
  }))
  registrations.push(monaco.languages.registerInlayHintsProvider('sos', {
    provideInlayHints: safe(async (model,range,token) => {
      const hints=await request(model,'textDocument/inlayHint',{range:toLSPRange(range)},token) || []
      return {hints:hints.map(h=>({
        position:{lineNumber:h.position.line+1,column:h.position.character+1},
        label:h.label,kind:h.kind===2?monaco.languages.InlayHintKind.Parameter:monaco.languages.InlayHintKind.Type,
        paddingLeft:h.paddingLeft,paddingRight:h.paddingRight,
        tooltip:typeof h.tooltip==='object'?{value:h.tooltip.value}:h.tooltip,
        textEdits:edits(h.textEdits)
      })),dispose(){}}
    })
  }))
  registrations.push(monaco.languages.registerCodeActionProvider('sos', {
    provideCodeActions: safe(async (model,range,_context,token) => {
      const actions=await request(model,'textDocument/codeAction',{range:toLSPRange(range),context:{diagnostics:[]}},token) || []
      return {actions:actions.flatMap(action=>{
        const changes=action.edit?.changes || {}
        const entries=Object.entries(changes)
        // The bridge's synthetic document is this buffer. Multi-file edits need a workspace UI.
        if (entries.some(([uri])=>!uri.endsWith('/buffer.sos') && uri!==model.uri.toString())) return []
        return [{title:action.title,kind:action.kind || 'quickfix',isPreferred:action.isPreferred,
          edit:{edits:entries.flatMap(([,values])=>values.map(e=>({resource:model.uri,versionId:model.getVersionId(),textEdit:{range:toEditorRange(e.range),text:e.newText}})))}}]
      }),dispose(){}}
    })
  },{providedCodeActionKinds:['quickfix']}))
  registrations.push(monaco.languages.registerFoldingRangeProvider('sos', {
    provideFoldingRanges: safe(async (model,_context,token) => {
      const ranges=await request(model,'textDocument/foldingRange',{},token) || []
      return ranges.map(r=>({start:r.startLine+1,end:r.endLine+1,kind:monaco.languages.FoldingRangeKind?.Region}))
    })
  }))
  return {
    refreshCodeLenses(){ for (const listener of codeLensListeners) listener() },
    dispose(){codeLensListeners.clear();registrations.forEach(r=>r.dispose())}
  }
}

function completionKind(monaco, kind) {
  const names={1:'Text',2:'Method',3:'Function',4:'Constructor',5:'Field',6:'Variable',7:'Class',8:'Interface',9:'Module',10:'Property',11:'Unit',12:'Value',13:'Enum',14:'Keyword',15:'Snippet',16:'Color',17:'File',18:'Reference',19:'Folder',20:'EnumMember',21:'Constant',22:'Struct',23:'Event',24:'Operator',25:'TypeParameter'}
  return monaco.languages.CompletionItemKind[names[kind] || 'Keyword']
}

// Core diagnostics count UTF-8 bytes; Monaco counts UTF-16 code units.
export function diagnosticColumn(line, byteColumn) {
 const limit=Math.max(0,byteColumn-1)
 let bytes=0, column=1
 for (const character of line) {
  const size=new TextEncoder().encode(character).length
  if (bytes+size>limit) break
  bytes+=size;column+=character.length
 }
 return column
}
