import {test} from 'node:test'
import assert from 'node:assert/strict'
import {installLanguageServices, SEMANTIC_LEGEND, diagnosticColumn} from './lsp.js'

function setup(api, analysis = () => null) {
  const providers={}
  const languages={CompletionItemKind:{Function:1,Keyword:2},InlayHintKind:{Type:1,Parameter:2},CompletionItemInsertTextRule:{InsertAsSnippet:4}}
  for (const kind of ['CodeLens','CompletionItem','Hover','Definition','DocumentFormattingEdit','DocumentSemanticTokens','InlayHints','CodeAction','FoldingRange']) {
    languages[`register${kind}Provider`]=(_language,provider)=>{providers[kind]=provider;return {dispose(){}}}
  }
  const model={getVersionId:()=>1,isDisposed:()=>false,getValue:()=>'',uri:{toString:()=> 'inmemory://model/1'},getWordUntilPosition:()=>({startColumn:6,endColumn:10})}
  const services=installLanguageServices({languages},api,()=>({}),analysis)
  return {providers,model,services}
}

test('code lenses show attributed Jev confidence and cost after analysis',async()=>{
  const decision={line:2,method:'jev',confidence:.94,input_tokens:721,usage_known:true}
  const {providers,model,services}=setup(async()=>({result:[{range:{start:{line:1,character:0},end:{line:1,character:1}},command:{command:'sos.analyze',title:'Semantic phrase · Analyze meaning'}}]}),()=>({decisions:[decision]}))
  let refreshes=0
  providers.CodeLens.onDidChange(()=>refreshes++)
  services.refreshCodeLenses()
  const result=await providers.CodeLens.provideCodeLenses(model)
  assert.equal(refreshes,1)
  assert.equal(result.lenses[0].command.title,'Jev · 94% confidence · 721 tokens · ~$0.00003028')
})

test('batched code lenses label shared usage instead of implying per-line spend',async()=>{
  const decision={line:2,method:'jev',confidence:.91,input_tokens:2400,usage_known:true,usage_shared:true,batch_size:8}
  const {providers,model}=setup(async()=>({result:[{range:{start:{line:1,character:0},end:{line:1,character:1}},command:{command:'sos.analyze',title:'Analyze'}}]}),()=>({decisions:[decision]}))
  const result=await providers.CodeLens.provideCodeLenses(model)
  assert.equal(result.lenses[0].command.title,'Jev · 91% confidence · 2400 tokens · ~$0.00010080 shared across 8 lines')
})

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
