#!/usr/bin/env python3
"""Installed Python agent-project consumer; private credentials never reach stdout."""
from __future__ import annotations
import hashlib
from importlib.metadata import version
import json
import os
from pathlib import Path
import re
import sys
import threading
from urllib.parse import urlsplit

from cutmyvideo import CutmyClient, CollectionError
import cutmyvideo


def read(path):
    value=json.loads(path.read_bytes())
    if not isinstance(value,dict):raise ValueError('invalid_acceptance_input')
    return value


def write(path,value):
    with path.open('x',encoding='utf-8') as stream:
        os.chmod(path,0o600)
        json.dump(value,stream,ensure_ascii=True)
        stream.flush();os.fsync(stream.fileno())


def emit(value):
    print(json.dumps(value,ensure_ascii=True,separators=(',',':')),flush=True)


def main():
    if version('cutmyvideo')!='1.4.0' or not Path(cutmyvideo.__file__).resolve().is_relative_to(Path(sys.prefix).resolve()):raise ValueError('requires_installed_release_wheel')
    action,input_name,work_name,base=sys.argv[1:]
    parsed=urlsplit(base)
    if parsed.scheme!='http' or parsed.hostname not in ('127.0.0.1','localhost','::1') or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError('requires_loopback')
    work=Path(work_name);work.mkdir(parents=True,exist_ok=True,mode=0o700)
    source_dir,collection=work/'source',work/'collection'
    def progress(report):
        if (action=='prepare-kill' and report['phase']=='uncertain') or (action=='repair-kill' and report['phase']=='submission_uncertain'):
            emit({'event':'checkpoint','checkpoint':report['phase'],'report':report})
            threading.Event().wait()
    with CutmyClient(base,request_timeout=30,download_timeout=120) as client:
        if action=='prepare-kill':
            client.prepare_source({'kind':'upload','path':str(Path(input_name).absolute())},source_dir,idempotency_key='agent-source-acceptance',timeout=600,on_progress=progress)
            raise ValueError('source_crash_checkpoint_missing')
        prepared=client.resume_source(source_dir,timeout=600)
        if action=='prepare-resume':emit(prepared);return
        request=read(work/'request.json')
        options={'timeout':600,'max_output_bytes':256<<20,'on_progress':progress}
        if action=='run-fail':
            try:client.run_collection(request,collection,idempotency_key='agent-project-acceptance',**options)
            except CollectionError as error:
                if len(error.report['outputs'])!=14 or len(error.report['pending'])!=1 or error.report['pending'][0].get('error_code')!='job_timeout':raise
                emit(error.report);return
            raise ValueError('expected_one_failed_clip')
        if action=='repair-kill':
            client.resume_collection(collection,retry_failed=['clip-15'],**options)
            raise ValueError('retry_crash_checkpoint_missing')
        if action in ('repair-resume','replay'):
            emit(client.resume_collection(collection,**options));return
        if action=='make-project':
            report=client.resume_collection(collection,**options)
            project=client.create_project({**request,'collection_key':'agent-project-acceptance','clip_jobs':[{'clip_id':o['clip_id'],'job_id':o['job_id']} for o in report['outputs']]})
            handoff=client.create_project_handoff(project['id'])
            write(work/'browser-private.json',{'base_url':base,'project_id':project['id'],'token':handoff['token']})
            emit({**report,'project_id':project['id']});return
        control=read(work/'browser-private.json')
        if action=='sync-project':
            emit(client.resume_collection(collection,project_id=control['project_id'],**options));return
        if action=='subtitles':
            report=client.resume_collection(collection,**options)
            source_text=(work/'supplied.srt').read_text(encoding='utf-8')
            response=client.retime_subtitles({'version':1,'format':'srt','text':source_text,
                'outputs':[{'clip_id':o['clip_id'],'segments':o['segments']} for o in report['outputs']]})
            directory=work/'sidecars';directory.mkdir(mode=0o700)
            sidecars=[]
            for item in response['outputs']:
                data=item['text'].encode('utf-8')
                if hashlib.sha256(data).hexdigest()!=item['sha256']:raise ValueError('sidecar_hash_mismatch')
                path=directory/(item['clip_id']+'.srt')
                with path.open('xb') as stream:os.chmod(path,0o600);stream.write(data);stream.flush();os.fsync(stream.fileno())
                sidecars.append({'clip_id':item['clip_id'],'path':str(path),'sha256':item['sha256'],'cue_count':item['cue_count']})
            emit({**report,'sidecars':sidecars});return
        raise ValueError('unknown_acceptance_action')


if __name__=='__main__':
    os.umask(0o077)
    try:main()
    except Exception as error:
        code=getattr(error,'code','agent_consumer_failure')
        if not isinstance(code,str) or not re.fullmatch('[a-z0-9_]{1,100}',code):code='agent_consumer_failure'
        print(json.dumps({'ok':False,'code':code,'exception_type':type(error).__name__}),file=sys.stderr)
        raise SystemExit(1)
