// Adapter fixture only; installed acceptance runs the actual CLI against media.
import assert from 'node:assert/strict';
import { readFile, stat } from 'node:fs/promises';
import { dirname, isAbsolute } from 'node:path';
const args = process.argv.slice(2), flag = name => args[args.indexOf(name)+1];
const base = {version:1,start_ms:1250,end_ms:3000,analyzed_end_ms:2500};
let result;
if(args[1]==='analyze') {
  assert(isAbsolute(flag('--input')));
  if(args.includes('--plan')) result={version:1,input:flag('--input'),ranges:[{start_ms:1250,end_ms:2000}],options:{format:flag('--format')},output_mode:flag('--output-mode')};
  else if(args[2]==='silence') result={...base,audio_track:1,silence:[{start_ms:2000,end_ms:2500}],keep:[{start_ms:1250,end_ms:2000,label:'Привет'}]};
  else if(args[2]==='video') result={...base,video_track:0,scenes:[{at_ms:2000,score:.9}],black:[]};
  else result={...base,audio_track:1,integrated_lufs:null,true_peak_dbtp:null,loudness_range_lu:null};
} else {
  assert.equal(args[1],'cutlist');assert(!args.includes('--timeout'));
  const inputFlag=['--input','--segments','--plan'].find(v=>args.includes(v)),path=flag(inputFlag),content=await readFile(path,'utf8');
  if(process.platform!=='win32'){assert.equal((await stat(path)).mode&0o777,0o600);assert.equal((await stat(dirname(path))).mode&0o777,0o700);}
  if(args[2]==='import'&&args.includes('--plan')) result={version:1,input:flag('--media'),ranges:[{start_ms:0,end_ms:Number(flag('--duration-ms'))}],options:{},output_mode:flag('--output-mode')};
  else if(args[2]==='import') result={version:1,entries:[{start_ms:0,end_ms:null,label:'Привет'}]};
  else result={version:1,format:flag('--format'),text:'0,,Привет\n',document:JSON.parse(content)};
  Object.assign(result,{input_text:content,temporary_path:path});
}
result.args=args;
if(process.env.SDK_BAD_MEDIA_RESPONSE)result=JSON.parse(process.env.SDK_BAD_MEDIA_RESPONSE);
console.log(JSON.stringify(result));
