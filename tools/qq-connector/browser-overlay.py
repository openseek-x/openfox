#!/usr/bin/env python3
"""Generate an explicit test-only overlay; never changes production sources.
Usage: browser-overlay.py OWNER_REPO CUMULATIVE_REPO EMPTY_OUTPUT_DIRECTORY
The harness supplies a 0600 loopback TLS descriptor. Node's sandbox denies all
other network access and unrelated HOME files; the actual SDK/hash check remains.
"""
import json
import pathlib
import sys
owner, target, output = map(lambda p: pathlib.Path(p).resolve(), sys.argv[1:])
output.mkdir(mode=0o700, parents=False, exist_ok=False)
runner = target / 'internal/integrations/qqconnector/runner.go'
source = runner.read_text()
needle = 'return c.run(ctx, onEvent, nil)'
if source.count(needle) != 1:
    raise SystemExit('unexpected fixed runner source')
(output / 'runner.go').write_text(source.replace(needle, 'return c.run(ctx, onEvent, browserSetup(c))'))
helper = r'''package qqconnector
import("encoding/json";"fmt";"os";"os/exec";"path/filepath";"strconv";"strings")
func browserSetup(c Config) testSetup {return func(home string, script []byte)([]byte,func(*exec.Cmd),error){
 var f struct{Port,CA string};b,e:=os.ReadFile(os.Getenv("OPENFOX_I14_NATIVE_TLS"));if e!=nil{return nil,nil,e};if e=json.Unmarshal(b,&f);e!=nil{return nil,nil,e};p,e:=strconv.Atoi(f.Port);if e!=nil||p<1||p>65535{return nil,nil,ErrUnverified}
 ca,_:=json.Marshal(f.CA)
 prefix:=`import fixtureHTTPS from 'node:https';const fixtureRequest=fixtureHTTPS.request.bind(fixtureHTTPS);fixtureHTTPS.request=(options,cb)=>{if(options.hostname!=='q.qq.com')throw new Error('fixture origin');return fixtureRequest({...options,hostname:'127.0.0.1',port:`+f.Port+`,servername:'example.com',ca:`+string(ca)+`,headers:{...options.headers,Host:'q.qq.com'}},cb)};`+"\n"
 var s strings.Builder;s.WriteString("(version 1)\n(deny default)\n(allow process-fork)\n(allow sysctl-read)\n")
 fmt.Fprintf(&s,"(allow process-exec (literal %s))\n",strconv.Quote(c.NodePath))
 s.WriteString("(allow file-read* (subpath \"/usr\") (subpath \"/System\") (subpath \"/Library/Apple\") (literal \"/dev/null\") (literal \"/dev/urandom\") (literal \"/dev/random\"))\n")
 s.WriteString("(allow file-read* (literal \"/\") (literal \"/private\") (literal \"/private/tmp\") (literal \"/dev\"))\n")
 fmt.Fprintf(&s,"(allow file-read* (literal %s) (subpath %s) (subpath %s))\n",strconv.Quote(c.NodePath),strconv.Quote(c.Root),strconv.Quote(home))
 fmt.Fprintf(&s,"(allow file-write* (subpath %s) (literal \"/dev/null\"))\n",strconv.Quote(home))
 for _,p:=range []string{c.NodePath,c.Root,home}{for p=filepath.Dir(p);;p=filepath.Dir(p){fmt.Fprintf(&s,"(allow file-read-metadata (literal %s))\n",strconv.Quote(p));if p=="/"{break}}}
 s.WriteString("(allow file-read-metadata (literal \"/tmp\") (literal \"/var\"))\n")
 fmt.Fprintf(&s,"(allow network-outbound (remote ip \"localhost:%s\"))\n",f.Port)
 policy:=filepath.Join(home,"sandbox.sb");if e=os.WriteFile(policy,[]byte(s.String()),0600);e!=nil{return nil,nil,e}
 return append([]byte(prefix),script...),func(cmd *exec.Cmd){cmd.Path="/usr/bin/sandbox-exec";cmd.Args=append([]string{"/usr/bin/sandbox-exec","-f",policy},cmd.Args...)},nil
}}
'''
(output / 'browser_overlay.go').write_text(helper)
replacements = {
    str(runner): str(output / 'runner.go'),
    str(target / 'internal/integrations/qqconnector/browser_overlay.go'): str(output / 'browser_overlay.go'),
    str(target / 'tests/integration/feishu/qq_browser_test.go'): str(owner / 'tests/integration/feishu/qq_browser_test.go'),
}
(output / 'overlay.json').write_text(json.dumps({'Replace': replacements}, indent=2) + '\n')
print(output / 'overlay.json')
