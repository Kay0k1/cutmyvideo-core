// Installed Node consumer. Never log private owner, project key or handoff token.
import { createHash } from 'node:crypto';
import { readFile, open, mkdir } from 'node:fs/promises';
import { join, dirname, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

function require(value, code) { if (!value) { const error=new Error(code);error.code=code;throw error; } }
async function read(path) { return JSON.parse(await readFile(path,'utf8')); }
async function write(path,value) {
  const f=await open(path,'wx',0o600);
  try {await f.writeFile(JSON.stringify(value));await f.sync();}finally{await f.close();}
}
function emit(value) {process.stdout.write(JSON.stringify(value)+'\n');}

async function main() {
  const [action,input,work,base]=process.argv.slice(2);
  const url=new URL(base);
  require(url.protocol==='http:'&&['127.0.0.1','localhost','[::1]'].includes(url.hostname)&&!url.username&&!url.password&&!url.search&&!url.hash,'requires_loopback');
  const entry=process.env.CUTMY_SDK_JS_ENTRY;
  require(entry&&process.env.CUTMY_SDK_COLLECTION_JS_ENTRY&&process.env.CUTMY_SDK_SOURCE_JS_ENTRY,'requires_installed_package');
  const {CutmyClient,CollectionError}=await import(pathToFileURL(entry).href);
  const {runCollection,resumeCollection}=await import(pathToFileURL(process.env.CUTMY_SDK_COLLECTION_JS_ENTRY).href);
  const {prepareSource,resumeSource}=await import(pathToFileURL(process.env.CUTMY_SDK_SOURCE_JS_ENTRY).href);
  const metadata=await read(join(dirname(entry),'../package.json'));
  require(metadata.name==='@cutmyvideo/sdk'&&metadata.version==='1.5.0','requires_release_package');
  await mkdir(work,{recursive:true,mode:0o700});
  const sourceDir=join(work,'source'),collection=join(work,'collection');
  const onProgress=async report=>{
    if(action==='prepare-kill'&&report.phase==='uncertain'||action==='repair-kill'&&report.phase==='submission_uncertain'){
      emit({event:'checkpoint',checkpoint:report.phase,report});
      // A live handle retains the consumer until the external harness kills it.
      await new Promise(()=>{setInterval(()=>{},1000);});
    }
  };
  const client=new CutmyClient({baseUrl:base,requestTimeoutMs:30000,transferTimeoutMs:120000});
  if(action==='prepare-kill'){
    await prepareSource(client,{kind:'upload',path:resolve(input)},sourceDir,{idempotencyKey:'agent-source-acceptance',timeoutMs:600000,onProgress});
    require(false,'source_crash_checkpoint_missing');
  }
  const prepared=await resumeSource(client,sourceDir,{timeoutMs:600000});
  if(action==='prepare-resume'){emit(prepared);return;}
  const request=await read(join(work,'request.json'));
  const options={timeoutMs:600000,maxOutputBytes:256<<20,onProgress};
  if(action==='run-fail'){
    try {await runCollection(client,request,collection,{...options,idempotencyKey:'agent-project-acceptance'});}
    catch(error){
      require(error instanceof CollectionError&&error.report.outputs.length===14&&error.report.pending.length===1&&error.report.pending[0].error_code==='job_timeout','expected_one_failed_clip');
      emit(error.report);return;
    }
    require(false,'expected_one_failed_clip');
  }
  if(action==='repair-kill'){
    await resumeCollection(client,collection,{...options,retryFailed:['clip-15']});
    require(false,'retry_crash_checkpoint_missing');
  }
  if(action==='repair-resume'||action==='replay'){emit(await resumeCollection(client,collection,options));return;}
  if(action==='make-project'){
    const report=await resumeCollection(client,collection,options);
    const project=await client.createProject({...request,collection_key:'agent-project-acceptance',clip_jobs:report.outputs.map(o=>({clip_id:o.clip_id,job_id:o.job_id}))});
    const handoff=await client.createProjectHandoff(project.id);
    await write(join(work,'browser-private.json'),{base_url:base,project_id:project.id,token:handoff.token});
    emit({...report,project_id:project.id});return;
  }
  const control=await read(join(work,'browser-private.json'));
  if(action==='sync-project'){emit(await resumeCollection(client,collection,{...options,projectId:control.project_id}));return;}
  if(action==='subtitles'){
    const report=await resumeCollection(client,collection,options),text=await readFile(join(work,'supplied.srt'),'utf8');
    const response=await client.retimeSubtitles({version:1,format:'srt',text,outputs:report.outputs.map(o=>({clip_id:o.clip_id,segments:o.segments}))});
    const directory=join(work,'sidecars');await mkdir(directory,{mode:0o700});
    const sidecars=[];
    for(const item of response.outputs){
      const data=Buffer.from(item.text),path=join(directory,item.clip_id+'.srt');
      require(createHash('sha256').update(data).digest('hex')===item.sha256,'sidecar_hash_mismatch');
      const f=await open(path,'wx',0o600);try{await f.writeFile(data);await f.sync();}finally{await f.close();}
      sidecars.push({clip_id:item.clip_id,path,sha256:item.sha256,cue_count:item.cue_count});
    }
    emit({...report,sidecars});return;
  }
  require(false,'unknown_acceptance_action');
}
try {await main();}
catch(error){const code=typeof error?.code==='string'&&/^[a-z0-9_]{1,100}$/.test(error.code)?error.code:'agent_consumer_failure';process.stderr.write(JSON.stringify({ok:false,code,exception_type:error?.name??'Error'})+'\n');process.exitCode=1;}
