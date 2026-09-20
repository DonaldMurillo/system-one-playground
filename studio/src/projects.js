// Project files remain on disk; unsaved buffers are retained while navigating.
export function installProjects({api, editor, monaco, load, onPath, report, busy=()=>false}) {
 const $=id=>document.getElementById(id)
 const buffers=new Map()
 let active='', mode='examples', loading=false, sequence=0, projectOpen=false, choosing=false
 let example={source:'',name:'script.sos'}
 const request=body=>api('/api/project',body)
 const status=text=>{$('project-status').textContent=text}
 function remember(){if(active&&!loading){const b=buffers.get(active);if(b)b.source=editor.getValue()}}
 function dirty(b){return b.source!==b.saved}
 editor.onDidChangeModelContent(()=>{remember();if(active){const b=buffers.get(active);status(active+(dirty(b)?' · unsaved':''))}})
 async function tree(){
  const result=await request({action:'tree'})
  const host=$('file-tree');host.replaceChildren()
  const folders=new Map([['',host]])
  for(const file of result.files){
   const parts=file.path.split('/'), name=parts.pop(), parent=parts.join('/')
   const container=folders.get(parent)||host
   if(file.directory){const details=document.createElement('details');details.open=parts.length<1;const summary=document.createElement('summary');summary.textContent=name;details.append(summary);container.append(details);folders.set(file.path,details)}
   else {const button=document.createElement('button');button.textContent=name;button.title=file.path;button.dataset.path=file.path;button.setAttribute('aria-current',file.path===active?'page':'false');button.onclick=()=>open(file.path).catch(report);container.append(button)}
  }
  return result
 }
 async function open(path){
  const seq=++sequence;remember()
  let buffer=buffers.get(path)
  if(!buffer){const r=await request({action:'read',path});if(seq!==sequence)return;buffer={source:r.source,saved:r.source,revision:r.revision};buffers.set(path,buffer)}
  if(seq!==sequence)return
  active=path;onPath(path);loading=true
  load(buffer.source,path)
  monaco.editor.setModelLanguage(editor.getModel(),path.endsWith('.sos')?'sos':path.endsWith('.json')?'json':'plaintext')
  loading=false;status(path+(dirty(buffer)?' · unsaved':''))
  for(const b of $('file-tree').querySelectorAll('button[data-path]'))b.setAttribute('aria-current',b.dataset.path===path?'page':'false')
 }
 async function save(){
  if(mode!=='studio')return false
  if(!projectOpen)return true
  remember()
  let path=active
  if(!path){path=window.prompt('Project-relative file path','main.sos');if(!path)return true;buffers.set(path,{source:editor.getValue(),saved:'',revision:''})}
  const b=buffers.get(path), source=b.source
  const result=await request({action:'write',path,source,revision:b.revision})
  b.saved=source;b.revision=result.revision
  active=path;onPath(path);status(path+(dirty(b)?' · unsaved':''));await tree();return true
 }
 function renderMode(){
  const welcome=mode==='studio'&&!projectOpen
  $('studio-mode').value=mode
  $('project-welcome').hidden=!welcome
  $('workbench').hidden=welcome
  $('project-build').hidden=mode!=='studio'||welcome
  $('explorer').hidden=mode!=='studio'||welcome
  $('examples').hidden=mode==='studio';$('examples-label').hidden=mode==='studio'
  for(const id of ['filename','open','format','save','analyze','stop','run','runbar','workdir','project-settings'])$(id).hidden=welcome
  $('choose-folder').hidden=welcome||!window.go?.main?.Desktop?.ChooseFolder
  $('cursor-pos').hidden=welcome;$('check-state').hidden=welcome
  editor.layout()
 }
 async function setMode(next,persist=true){
  if(busy()){ $('studio-mode').value=mode;throw new Error('Stop Run or Analyze before switching modes.') }
  remember();sequence++
  if(mode==='examples')example={source:editor.getValue(),name:$('filename').value}
  if(persist)await request({action:'settings',mode:next})
  mode=next;projectOpen=false;active='';onPath('');loading=true
  load(next==='studio'?'':example.source,next==='studio'?'untitled.sos':example.name)
  monaco.editor.setModelLanguage(editor.getModel(),'sos');loading=false
  $('file-tree').replaceChildren();status('');$('welcome-status').textContent=''
  renderMode()
  if(next==='studio')$('welcome-open').focus()
 }
 async function environment(){const r=await request({action:'environment'});$('environment-list').replaceChildren();for(const v of r.variables){const row=document.createElement('div');row.textContent=`${v.name} · ${v.configured?'configured':'not set'} (${v.origin})`;$('environment-list').append(row)}}
 $('project-build').onclick=async()=>{
  try {
   if(!active.endsWith('.sos'))throw new Error('Select a .sos entry point to build.')
   remember();if(mode==='examples')example={source:editor.getValue(),name:$('filename').value}
  if([...buffers.values()].some(dirty))throw new Error('Save edited project files before building.')
   const output=window.prompt('Project-relative executable output path','app');if(!output)return
   $('project-build').disabled=true;status('Building native CLI…')
   const r=await api('/api/build',{path:active,output});status('Built '+(r.output||output));await tree()
  }catch(e){report(e)}finally{$('project-build').disabled=false}
 }
 $('studio-mode').onchange=async()=>{const next=$('studio-mode').value;$('studio-mode').disabled=true;try{await setMode(next)}catch(e){renderMode();report(e)}finally{$('studio-mode').disabled=false}}
 $('tree-refresh').onclick=()=>tree().catch(report)
 $('file-new').onclick=async()=>{const path=window.prompt('New project-relative file path','new.sos');if(!path)return;try{await request({action:'write',path,source:'',revision:''});await tree();await open(path)}catch(e){report(e)}}
 $('folder-new').onclick=async()=>{const path=window.prompt('New project-relative folder','lib');if(!path)return;try{await request({action:'mkdir',path});await tree()}catch(e){report(e)}}
 $('project-settings').onclick=()=>{$('settings-status').textContent='';$('settings-dialog').showModal();environment().catch(e=>{$('settings-status').textContent=e.message})}
 $('environment-form').onsubmit=async e=>{e.preventDefault();try{await request({action:'setEnvironment',name:$('environment-name').value,value:$('environment-value').value});$('environment-value').value='';$('settings-status').textContent='Saved. Used by the next Run or Analyze.';await environment()}catch(err){$('settings-status').textContent=err.message}}
 async function pickFolder(){
  const dialog=$('folder-dialog')
  let selected='',parent='',browseSequence=0
  async function browse(path){
   const seq=++browseSequence
   $('folder-select').disabled=true;$('folder-status').textContent='Loading folders…'
   try{
    const r=await request({action:'browseFolders',path})
    if(seq!==browseSequence||!dialog.open)return
    selected=r.path;parent=r.parent;$('folder-location').textContent=selected
    $('folder-up').disabled=parent===selected;$('folder-list').replaceChildren()
    for(const folder of r.folders){const button=document.createElement('button');button.type='button';button.textContent='▸ '+folder.name;button.onclick=()=>browse(folder.path);$('folder-list').append(button)}
    $('folder-status').textContent=r.folders.length?'':'No subfolders. You can open this folder.'
    $('folder-select').disabled=false
   }catch(e){if(seq===browseSequence&&dialog.open)$('folder-status').textContent=e.message}
  }
  return new Promise(resolve=>{
   dialog.returnValue='';dialog.showModal()
   dialog.addEventListener('close',()=>{browseSequence++;resolve(dialog.returnValue==='open'?selected:null)},{once:true})
   $('folder-home').onclick=()=>browse('');$('folder-up').onclick=()=>browse(parent)
   $('folder-select').onclick=()=>dialog.close('open')
   browse($('workdir').textContent)
  })
 }
 async function changeRoot(){
  if(choosing)return
  if(busy())throw new Error('Stop Run or Analyze before opening a project.')
  remember();if(mode==='examples')example={source:editor.getValue(),name:$('filename').value}
  if([...buffers.values()].some(dirty)&&!window.confirm('This project has unsaved files. Discard them and switch folders?'))return
  choosing=true;$('welcome-open').disabled=true;$('welcome-status').textContent=''
  try{
   if(window.go?.main?.Desktop?.ChooseFolder){const chosen=await window.go.main.Desktop.ChooseFolder();if(!chosen)return}
   else {const path=await pickFolder();if(!path)return;await request({action:'openProject',path})}
   const session=await api('/api/session',undefined,'GET');$('workdir').textContent=session.dir
   buffers.clear();active='';sequence++;onPath('');mode='studio';projectOpen=true
   loading=true;load('','untitled.sos');loading=false
   await request({action:'settings',mode});renderMode()
   const result=await tree()
   const first=result.files.find(f=>f.path==='main.sos')||result.files.find(f=>!f.directory&&f.path.endsWith('.sos'))
   if(first)await open(first.path)
   else status('Choose a file or create a new script.')
   editor.layout()
  }finally{choosing=false;$('welcome-open').disabled=false}
 }
 $('welcome-open').onclick=()=>changeRoot().catch(e=>{$('welcome-status').textContent=e.message})
 $('project-open').onclick=()=>changeRoot().catch(report)
 return {save,open,changeRoot,tree,hasUnsavedImports:()=>[...buffers.entries()].some(([path,b])=>path!==active&&dirty(b)),async init(){const r=await request({action:'settings'});if(r.mode==='studio')await setMode('studio',false)},isProject:()=>mode==='studio',hasDocument:()=>mode!=='studio'||projectOpen}
}
