import assert from 'node:assert/strict';
import { stat, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';
const { CutmyClient, ApiError, exportPortableProject, importPortableProject }=await import(process.env.CUTMY_SDK_JS_ENTRY?pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href:'../dist/index.js');
const { LocalCutmy, LocalError }=await import(process.env.CUTMY_SDK_LOCAL_JS_ENTRY?pathToFileURL(process.env.CUTMY_SDK_LOCAL_JS_ENTRY).href:'../dist/local.js');
const fixture=fileURLToPath(new URL('./media-cli.mjs',import.meta.url));
const local=(env={})=>new LocalCutmy({binary:process.execPath,binaryArgs:[fixture],threads:3,env});
const base={version:1,start_ms:0,end_ms:1000,analyzed_end_ms:900};
const silence={...base,audio_track:1,silence:[],keep:[{start_ms:0,end_ms:900,label:''}]};
const video={...base,video_track:0,scenes:[],black:[]};
const loudness={...base,audio_track:1,integrated_lufs:null,true_peak_dbtp:null,loudness_range_lu:null};
const source={id:'own',duration_ms:1000};
const limits={max_source_bytes:1000000,max_output_bytes:1000000,max_fetch_bytes:1000000,max_ranges:128,max_range_ms:100000,max_job_ms:100000};

test('local analyses preserve exact milliseconds, literal paths, defaults and nullable loudness',async()=>{
 const opts={start_ms:1250,end_ms:0,threshold_db:-40,keep_before_ms:0},result=await local().analyzeSilence('input ; $(unsafe) Привет.mp4',opts);
 assert.deepEqual(result.args.slice(0,3),['--json-errors','analyze','silence']);assert.equal(result.args[result.args.indexOf('--start')+1],'1.250');assert(!result.args.includes('--end'));assert.equal(result.args[result.args.indexOf('--threads')+1],'3');assert.equal(opts.end_ms,0);
 assert.equal((await local().analyzeVideo('input')).scenes[0].at_ms,2000);assert.equal((await local().analyzeLoudness('input')).integrated_lufs,null);
 const plan=await local().silencePlan('input',{}, {format:'mp3',outputMode:'separate'});assert.equal(plan.options.format,'mp3');assert.equal(plan.output_mode,'separate');
});
test('local cut lists retain Unicode markers, private files and all three export modes',async()=>{
 const request={format:'csv',text:'0,,Привет\n'},cut=await local().importCutList(request);assert.equal(cut.entries[0].end_ms,null);assert.equal(cut.input_text,request.text);await assert.rejects(stat(cut.temporary_path),e=>e.code==='ENOENT');
 const plan=await local().planCutList(request,{media:'video.mp4',durationMs:1000});assert.equal(plan.ranges[0].end_ms,1000);
 const inputs={cut_list:{version:1,entries:cut.entries},plan:Object.fromEntries(['version','input','ranges','options'].map(k=>[k,plan[k]])),segments:[{index:0,label:'Привет',source_start_ms:1,source_end_ms:2,output_start_ms:0,output_end_ms:1}]};
 for(const [key,value]of Object.entries(inputs)){const result=await local().exportCutList({format:'csv',[key]:value});assert.deepEqual(result.document,value);await assert.rejects(stat(result.temporary_path),e=>e.code==='ENOENT');}
});
test('HTTP analyses and cut-list methods send exact endpoint/body and retain actual coverage',async()=>{
 const calls=[],list={version:1,entries:[{start_ms:0,end_ms:null,label:'Привет'}]};
 const client=new CutmyClient({baseUrl:'http://127.0.0.1',fetch:async(url,init)=>{
  const path=new URL(url).pathname;calls.push({path,body:init.body===undefined?undefined:JSON.parse(init.body)});
  let data=path.endsWith('/session')?{ok:true,limits}:path.endsWith('/silence')?silence:path.endsWith('/video')?video:path.endsWith('/loudness')?loudness:path.endsWith('/import')?list:{version:1,format:'csv',text:'0,,Привет\n'};
  return new Response(JSON.stringify(data),{headers:{'Content-Type':'application/json'}});
 }});
 for(const [name,result]of [['Silence',silence],['Video',video],['Loudness',loudness]])assert.deepEqual(await client['analyze'+name]('src',{start_ms:0,end_ms:1000}),result);
 assert.deepEqual(await client.importCutList({format:'csv',text:'0,,Привет\n'}),list);await client.exportCutList({format:'csv',cut_list:list});
 assert.deepEqual(calls.slice(1).map(r=>r.path),['/api/v1/sources/src/analysis/silence','/api/v1/sources/src/analysis/video','/api/v1/sources/src/analysis/loudness','/api/v1/cut-lists/import','/api/v1/cut-lists/export']);
 assert.deepEqual(calls[1].body,{start_ms:0,end_ms:1000});
});
test('malformed coverage, nonfinite metrics, null options and wrong response kinds are refused',async()=>{
 for(const result of [{...silence,analyzed_end_ms:1001},{...silence,silence:null},{...silence,keep:[{start_ms:0,end_ms:950,label:''}]},{...silence,start_ms:true}])await assert.rejects(local({SDK_BAD_MEDIA_RESPONSE:JSON.stringify(result)}).analyzeSilence('input'),e=>e instanceof LocalError&&e.code==='invalid_response');
 let called=false;const client=new CutmyClient({baseUrl:'http://127.0.0.1',fetch:async()=>{called=true;throw Error('unexpected');}});
 for(const opts of [{threshold_db:NaN},{start_ms:true},{audio_track:null},{future:1}])await assert.rejects(client.analyzeSilence('src',opts),TypeError);assert.equal(called,false);
 await assert.rejects(client.exportCutList({format:'csv',cut_list:{},segments:[]}),TypeError);
 assert.throws(()=>client.preflight({audio_replace:{input:'/private/file'}}),TypeError);
 await assert.rejects(local().preflight({version:1,input:'primary',ranges:[{start_ms:0,end_ms:1}],options:{audio_replace:{source_id:'foreign'}}},'.'),TypeError);
});
test('portable render choices round-trip without accepting replacement IDs or local paths',()=>{
 const options={audio_normalize:{integrated_lufs:-16,true_peak_dbtp:-1,loudness_range_lu:11},canvas:{width:1080,height:1920,fit:'contain',background:'#000000'}},request={source_id:'own',plan:{version:1,options,clips:[{id:'answer',ranges:[{start_ms:0,end_ms:500}],options:{clear_audio_normalize:true,clear_canvas:true,clear_audio_replace:true}}]}};
 const portable=exportPortableProject(request,source);assert.deepEqual(importPortableProject(portable,source),request);
 for(const replacement of [{source_id:'private-audio'},{input:'/private/audio.wav'}]){
  const bad=structuredClone(request);bad.plan.clips[0].options.audio_replace=replacement;assert.throws(()=>exportPortableProject(bad,source),TypeError);
  const document=structuredClone(portable);document.plan.options.audio_replace=replacement;assert.throws(()=>importPortableProject(document,source),TypeError);
 }
});

test('local collection replacement paths resolve before moving the plan without mutating the caller',async()=>{
 const helper=fileURLToPath(new URL('./fake-cli.mjs',import.meta.url)),client=new LocalCutmy({binary:process.execPath,binaryArgs:[helper]});
 const plan={version:1,input:'primary.mp4',options:{audio_replace:{input:'audio Привет.wav',offset_ms:250}},clips:[{id:'answer',ranges:[{start_ms:0,end_ms:500}],options:{audio_replace:{input:'override.wav'}}}]},original=structuredClone(plan);
 const result=await client.validateCollection(plan,'.',{clipIds:['answer']});
 assert.equal(result.plan.options.audio_replace.input,resolve('audio Привет.wav'));assert.equal(result.plan.clips[0].options.audio_replace.input,resolve('override.wav'));assert.deepEqual(plan,original);
 await assert.rejects(stat(result.plan_path),e=>e.code==='ENOENT');
});


test('nonfinite render numbers cannot turn into null defaults during request or plan copying',async()=>{
 const { runCollection }=await import(process.env.CUTMY_SDK_COLLECTION_JS_ENTRY?pathToFileURL(process.env.CUTMY_SDK_COLLECTION_JS_ENTRY).href:'../dist/collections.js');
 let called=false;const client=new CutmyClient({baseUrl:'http://127.0.0.1',fetch:async()=>{called=true;throw Error('unexpected network');}});
 const ranges=[{start_ms:0,end_ms:1000}],base={source_id:'own',ranges,format:'mp4',quality:'best',cut_mode:'accurate'};
 for(const edit of [
  {audio_normalize:{integrated_lufs:-16,true_peak_dbtp:NaN,loudness_range_lu:11}},
  {canvas:{width:Infinity,height:160,fit:'contain',background:'#000000'}},
  {audio_replace:{source_id:'audio',offset_ms:NaN}},
 ]){
  assert.throws(()=>client.preflight({...base,...edit}),/finite/);
  assert.throws(()=>client.createJob({...base,...edit},{idempotencyKey:'never-submitted'}),/finite/);
  const localEdit=edit.audio_replace?{audio_replace:{input:'audio.wav',offset_ms:NaN}}:edit;
  await assert.rejects(local().preflight({version:1,input:'primary.mp4',ranges,options:localEdit},'output.mp4'),/finite/);
  const request={source_id:'own',plan:{version:1,options:edit,clips:[{id:'answer',ranges}]}};
  assert.throws(()=>exportPortableProject(request,source),/finite/);
  const document={kind:'cutmy.collection.project',version:1,source:{duration_ms:1000},plan:request.plan};
  assert.throws(()=>importPortableProject(document,source),/finite/);
  const directory=await mkdtemp(join(tmpdir(),'cutmy-nonfinite-render-'));
  try{await assert.rejects(runCollection(client,request,directory,{timeoutMs:1000}),error=>error.code==='invalid_request');}
  finally{await rm(directory,{recursive:true,force:true});}
 }
 assert.equal(called,false);
});
