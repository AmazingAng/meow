#!/usr/bin/env python3
"""Verify notification modules without changing installed agents or using a simulator."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
CORE = ['message/normalize.go', 'message/preview.go', 'adapters/adapter.go',
        'adapters/claude.go', 'adapters/codex.go', 'adapters/format.go', 'adapters/service.go']


def run(args, out, name, *, cwd=ROOT, env=None, stdin=None, check=True):
    with (out / (name + '.log')).open('w') as log:
        p = subprocess.run(args, cwd=cwd, env=env, input=stdin, text=True,
                           stdout=log, stderr=subprocess.STDOUT)
    if check and p.returncode:
        raise RuntimeError(f'{name} failed; see {out / (name + ".log")}')
    return p.returncode


def core_gate(records):
    if set(records) != set(CORE) or any(not rows for rows in records.values()):
        raise RuntimeError('missing core coverage records')
    uncovered = [(name, line) for name, rows in records.items() for line, count in rows if count == 0]
    if uncovered: raise RuntimeError('uncovered core blocks: ' + repr(uncovered))
    return {name: len(rows) for name, rows in records.items()}


def coverage(out):
    records = {name: [] for name in CORE}
    for line in (out / 'go.cover').read_text().splitlines():
        match = re.match(r'meow.blue/meow/(.+):(\d+)\.\d+,\d+\.\d+ \d+ (\d+)$', line)
        if match and match[1] in records: records[match[1]].append((int(match[2]), int(match[3])))
    result = core_gate(records)
    for bad in [{}, {name:[(line,0) for line,_ in rows] for name,rows in records.items()}]:
        try: core_gate(bad)
        except RuntimeError: pass
        else: raise RuntimeError('coverage gate accepted known-bad input')
    return {'covered_blocks': result, 'negative_controls_rejected':2,
            'scope':'All executable core protocol/adapter blocks; host IO and SwiftUI runtime are not included.'}


def boundaries(out):
    result = {}
    for module in ['message','adapters']:
        raw = subprocess.check_output(['go','list','-json','./'+module], cwd=ROOT, text=True)
        doc = json.loads(raw)
        forbidden = {'os','os/exec','net','net/http','database/sql'}
        if forbidden.intersection(doc.get('Imports',[])):
            raise RuntimeError('protocol/adapter module acquired host IO: ' + module)
        own_imports = [name for name in doc.get('Imports',[]) if name.startswith('meow.blue/meow/')]
        if module=='message' and own_imports: raise RuntimeError('core protocol depends on outer module')
        if module=='adapters' and any(name!='meow.blue/meow/message' for name in own_imports):
            raise RuntimeError('adapters depend on transport or host')
        result[module] = doc.get('Imports',[])
    (out/'boundaries.json').write_text(json.dumps(result,indent=2)+'\n')
    return result


def native(out):
    spec = importlib.util.spec_from_file_location('context_checks', ROOT/'scripts/verify_session_context.py')
    helper = importlib.util.module_from_spec(spec); spec.loader.exec_module(helper)
    helper.native_package(out)
    shutil.copy2(ROOT/'ios/MeowTests/ProtocolContractTests.swift',out/'package/Tests/ProtocolContractTests.swift')
    shutil.copy2(ROOT/'contracts/fixtures/notifications.json',out/'package/Tests/notifications.json')
    manifest = out/'package/Package.swift'
    text = manifest.read_text()
    old = 'path: "Tests")'
    if text.count(old)!=1: raise RuntimeError('test package resource target changed')
    manifest.write_text(text.replace(old,'path: "Tests", resources: [.copy("notifications.json"), .copy("meow.wav")])'))
    # The extra test bundle's Bundle.module changes Swift's extension lookup.
    # Keep the existing sound regression pointed at the identical bundled bytes.
    shutil.copy2(ROOT/'ios/Meow/Resources/Sounds/meow.wav',out/'package/Tests/meow.wav')
    run(['swift','test','--package-path',str(out/'package')],out,'native')
    log=(out/'native.log').read_text()
    cases=re.findall(r"Test Case '.+' passed",log)
    if not cases or "Test Suite 'All tests' passed" not in log or 'ProtocolContractTests' not in log:
        raise RuntimeError('missing native contract execution')
    return len(cases)


def mutants(out):
    cases = [
        ('default-priority','message/normalize.go','n.Priority = PriorityNormal','n.Priority = PriorityHigh','./message','TestProtocolFixtures|TestNormalization'),
        ('event-body-key','adapters/adapter.go','e.EventID, string(b)','e.EventID, string(b[:0])+"same"','./adapters','TestFrozenAgentNotifications'),
        ('context-identity','adapters/format.go','SessionID: message.CleanLabel(o.SessionID, message.MaxContextID)',
         'SessionID: ""','./adapters','TestFrozenAgentNotifications|TestCustomOrigin'),
        ('preview-limit','message/preview.go','return Truncate(text, MaxPreviewRunes)','return text','./message','TestPreviewContractAndUtilities'),
    ]
    results=[]
    with tempfile.TemporaryDirectory(prefix='meow-modules-mutants-') as folder:
        root=Path(folder)
        for name in ['cli','client','message','adapters','contracts','server']:
            shutil.copytree(ROOT/name,root/name)
        shutil.copytree(ROOT/'ios/MeowTests/Fixtures',root/'ios/MeowTests/Fixtures')
        for name in ['go.mod','go.sum']: shutil.copy2(ROOT/name,root/name)
        for name,filename,old,new,package,selector in cases:
            file=root/filename;original=file.read_text()
            expected=1
            if original.count(old)!=expected: raise RuntimeError('ambiguous mutant '+name)
            changed=original.replace(old,new);file.write_text(changed)
            try:
                code=run(['go','test',package,'-run',selector,'-count=1','-json'],out,'mutant-'+name,cwd=root,check=False)
                events=[json.loads(x) for x in (out/('mutant-'+name+'.log')).read_text().splitlines() if x.startswith('{')]
                failed=[e['Test'] for e in events if e.get('Action')=='fail' and e.get('Test')]
                if code!=1 or not failed: raise RuntimeError('mutant survived or did not execute '+name)
                results.append({'name':name,'failed_tests':failed})
            finally: file.write_text(original)
    return results


def live(out):
    run(['go','build','-o',str(out/'meow'),'./cli/cmd/meow'],out,'cli-build')
    run(['go','build','-o',str(out/'meow-server'),'./server/cmd/meow-server'],out,'server-build')
    with tempfile.TemporaryDirectory(prefix='meow-modules-live-') as tmp:
        d=Path(tmp)
        with socket.socket() as sock: sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
        origin=f'http://127.0.0.1:{port}'
        env=dict(os.environ,HOME=str(d),CODEX_HOME=str(d/'codex'),CLAUDE_CONFIG_DIR=str(d/'claude'),
                 MEOW_CONFIG=str(d/'meow.json'),MEOW_SERVER=origin,MEOW_ADDR=f'127.0.0.1:{port}',
                 MEOW_DB=str(d/'meow.db'),MEOW_APNS_KEY_FILE='',MEOW_PUSH_GATEWAY_URL='',
                 MEOW_HOOK_DISABLED='0',MEOW_LANGUAGE='en')
        def request(path,data=None,token=''):
            req=urllib.request.Request(origin+path,data=None if data is None else json.dumps(data).encode(),
                headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
            with urllib.request.urlopen(req,timeout=3) as response:
                raw=response.read();return raw if path=='/healthz' else json.loads(raw)
        with (out/'live-server.log').open('w') as log:
            server=subprocess.Popen([str(out/'meow-server')],env=env,stdout=log,stderr=log)
            try:
                for _ in range(100):
                    if server.poll() is not None: raise RuntimeError('server stopped before readiness')
                    try: request('/healthz');break
                    except OSError: time.sleep(.05)
                else: raise RuntimeError('server not ready')
                reg=request('/v1/devices',{'name':'module contract test'});env['MEOW_KEY']=reg['key']['secret']
                payload=json.dumps({'title':'Deployment completed','source':'CI','body':'**All tests passed** 😺',
                                    'context':{'workspace':'meow','session':'Release checks','session_id':'ci-run-1'}},ensure_ascii=False)
                args=[str(out/'meow'),'send','--json','--idempotency-key','ci-run-1']
                run(args,out,'live-json-stdin',env=env,stdin=payload)
                run(args+[payload],out,'live-json-replay',env=env)
                page=request('/v1/inbox',token=reg['device_token'])['notifications']
                if len(page)!=1 or page[0]['context']['session_id']!='ci-run-1' or page[0]['body']!='**All tests passed** 😺':
                    raise RuntimeError('service JSON lost content or idempotency')
                hook=json.dumps({'hook_event_name':'Stop','session_id':'s1','prompt_id':'p1','cwd':'/private/work/meow',
                                 'session_title':'Agent session','last_assistant_message':'Original\nbody'})
                run([str(out/'meow'),'hook','claude-stop'],out,'live-agent',env=env,stdin=hook)
                final=request('/v1/inbox',token=reg['device_token'])['notifications']
                if len(final)!=2 or not any(n['source']=='Claude Code' and n['context']['session']=='Agent session' for n in final):
                    raise RuntimeError('agent path did not produce standard notification')
                return {'service_json':True,'retry_created_once':True,'agent_notification':True,'messages':2}
            finally:
                server.terminate()
                try: server.wait(timeout=5)
                except subprocess.TimeoutExpired: server.kill();server.wait()


def docker_runtime(out, image):
    container=subprocess.check_output(['docker','run','--detach','--rm','--tmpfs','/data:uid=65532,gid=65532',
        '--publish','127.0.0.1::8080',image],text=True).strip()
    try:
        ports=json.loads(subprocess.check_output(['docker','inspect','--format','{{json .NetworkSettings.Ports}}',container],text=True))
        mapping=ports['8080/tcp'][0]
        if mapping['HostIp']!='127.0.0.1':raise RuntimeError('test container unexpectedly exposed publicly')
        origin='http://127.0.0.1:'+mapping['HostPort']
        def request(path,data=None,token='',key=''):
            req=urllib.request.Request(origin+path,data=None if data is None else json.dumps(data).encode(),
                headers={'Authorization':'Bearer '+token,'Content-Type':'application/json','Idempotency-Key':key})
            with urllib.request.urlopen(req,timeout=3) as res:
                raw=res.read();return raw if path=='/healthz' else json.loads(raw)
        for _ in range(100):
            try:request('/healthz');break
            except OSError:time.sleep(.05)
        else:raise RuntimeError('container did not become healthy')
        reg=request('/v1/devices',{'name':'Docker module test'})
        payload={'title':'Container test','body':'**Shared protocol** 😺','source':'CI',
                 'context':{'workspace':'meow','session':'Container check','session_id':'docker-run-1'}}
        first=request('/v1/notifications',payload,reg['key']['secret'],'docker-run-1')
        again=request('/v1/notifications',payload,reg['key']['secret'],'docker-run-1')
        rows=request('/v1/inbox',token=reg['device_token'])['notifications']
        if first['id']!=again['id'] or len(rows)!=1 or rows[0]['context']['session_id']!='docker-run-1':
            raise RuntimeError('container protocol/idempotency failed')
        run(['docker','exec',container,'/meow-server','healthcheck'],out,'docker-healthcheck')
        return {'healthcheck':True,'notification_context':True,'idempotent_replay':True,'loopback_only':True}
    finally:
        run(['docker','rm','--force',container],out,'docker-cleanup')


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--output',type=Path,required=True)
    p.add_argument('--docker',action='store_true',help='also build and smoke-test the server image')
    args=p.parse_args();out=args.output.resolve();out.mkdir(parents=True,exist_ok=False)
    run(['go','test','-race','-shuffle=on','-count=1','-json','-coverprofile='+str(out/'go.cover'),'./...'],out,'go')
    run(['go','vet','./...'],out,'vet')
    run(['go','test','./message','-run','^$','-fuzz','^FuzzNormalizeRoundTrip$','-fuzztime','2s'],out,'fuzz')
    result={'boundaries':boundaries(out),'coverage':coverage(out),'native_tests':native(out),'mutants':mutants(out),'live':live(out)}
    run(['xcodegen','generate','--spec','ios/project.yml'],out,'xcodegen')
    run(['xcodebuild','build-for-testing','-project','ios/Meow.xcodeproj','-scheme','Meow','-destination','generic/platform=iOS',
         '-derivedDataPath',str(out/'ios-build'),'CODE_SIGNING_ALLOWED=NO'],out,'ios-compile')
    result['ios_sdk_compile']=True
    if args.docker:
        image='meow-server:notification-modules-check'
        run(['docker','build','--progress=plain','-t',image,'.'],out,'docker-build')
        result['docker']=docker_runtime(out,image)
    (out/'results.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps(result,ensure_ascii=False))


if __name__=='__main__':main()
