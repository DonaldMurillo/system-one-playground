import {test} from 'node:test'
import assert from 'node:assert/strict'
import {installLanguageServices, SEMANTIC_LEGEND, diagnosticColumn} from './lsp.js'

function setup(api) {
  const providers={}
  const languages={CompletionItemKind:{Function:1,Keyword:2},InlayHintKind:{Type:1,Parameter:2},CompletionItemInsertTextRule:{InsertAsSnippet:4}}
  for (const kind of ['CompletionItem','Hover','Definition','DocumentFormattingEdit','DocumentSemanticTokens','InlayHints','CodeAction','FoldingRange']) {
    languages[`register${kind}Provider`]=(_language,provider)=>{providers[kind]=provider;return {dispose(){}}}
  }
  const model={getVersionId:()=>1,isDisposed:()=>false,getValue:()=>'',uri:{toString:()=> 'inmemory://model/1'},getWordUntilPosition:()=>({startColumn:6,endColumn:10})}
  installLanguageServices({languages},api)
  return {providers,model}
}

test('completion preserves replacement ranges and atomic import edits',async()=>{
  const {providers,model}=setup(async()=>({result:[{label:'text.trim',kind:3,detail:'std/text',textEdit:{range:{start:{line:1,character:5},end:{line:1,character:14}},newText:'text.trim'},additionalTextEdits:[{range:{start:{line:0,character:0},end:{line:0,character:0}},newText:'import "std/text"\n'}]}]}))
  const r=await providers.CompletionItem.provideCompletionItems(model,{lineNumber:2,column:10})
  assert.equal(r.suggestions[0].range.endColumn,15)
  assert.equal(r.suggestions[0].additionalTextEdits[0].text,'import "std/text"\n')
  assert.equal(r.suggestions[0].kind,1)
})
test('stale responses cannot insert imports into a changed buffer',async()=>{
  let version=1
  const {providers,model}=setup(async()=>{version=2;return {result:[{label:'text.trim'}]}})
  model.getVersionId=()=>version
  const r=await providers.CompletionItem.provideCompletionItems(model,{lineNumber:1,column:10})
  assert.deepEqual(r.suggestions,[])
})
test('semantic data and inlay hints use editor coordinate conventions',async()=>{
  const {providers,model}=setup(async(_path,req)=>({result:req.method.includes('semanticTokens')?{data:[0,0,4,0,0]}:[{position:{line:0,character:10},label:': text',kind:1}]}))
  assert.deepEqual(providers.DocumentSemanticTokens.getLegend(),SEMANTIC_LEGEND)
  assert.ok((await providers.DocumentSemanticTokens.provideDocumentSemanticTokens(model)).data instanceof Uint32Array)
  const result=await providers.InlayHints.provideInlayHints(model,{startLineNumber:1,startColumn:1,endLineNumber:2,endColumn:1})
  assert.deepEqual(result.hints[0].position,{lineNumber:1,column:11})
})
test('quickfix imports map to the active model as a versioned workspace edit',async()=>{
  const {providers,model}=setup(async()=>({result:[{title:'Import std/text',edit:{changes:{'file:///sysonescript/buffer.sos':[{range:{start:{line:0,character:0},end:{line:0,character:0}},newText:'import "std/text"\n'}]}}}]}))
  const r=await providers.CodeAction.provideCodeActions(model,{startLineNumber:1,startColumn:1,endLineNumber:1,endColumn:10})
  assert.equal(r.actions[0].edit.edits[0].resource,model.uri)
  assert.equal(r.actions[0].edit.edits[0].versionId,1)
})

test('diagnostics convert UTF8 byte columns to Monaco UTF16 columns',()=>{
 assert.equal(diagnosticColumn('é😀x',7),4)
 assert.equal(diagnosticColumn('é😀x',3),2)
 assert.equal(diagnosticColumn('é😀x',1),1)
})
