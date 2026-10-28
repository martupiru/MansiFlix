#!/usr/bin/env python3
"""Explicitly authorized, isolated Video Lab identity. No credential output/storage.
Creates new Secret/policy ConfigMap/Job only. Never replaces a resource or identity.
"""
import json, os, secrets, subprocess, sys, uuid
from pathlib import Path

os.environ['KUBECONFIG']='/Users/maurosorbello/Documents/Middleware/kubeconfig'
os.umask(0o077)
base=['kubectl','--context=k0s-cluser','-n','mansiflix','--request-timeout=20s']
root=Path(__file__).resolve().parent

def call(args,payload=None):
    r=subprocess.run(base+args,input=payload,text=True,capture_output=True,timeout=30)
    if r.returncode: sys.exit('Kubernetes operation failed (details suppressed): '+' '.join(args[:2]))
    return r.stdout

def create(obj):
    kind,name=obj['kind'],obj['metadata']['name']
    if call(['get',kind,name,'--ignore-not-found','-o','name']).strip(): sys.exit('Refusing existing name: '+name)
    response=json.loads(call(['create','-f','-','-o','json'],json.dumps(obj)))
    print('CREATED',kind,name,'UID',response['metadata']['uid'],flush=True)
    return {'kind':kind,'name':name,'uid':response['metadata']['uid']}

if __name__=='__main__':
    if '--authorized-new-identity' not in sys.argv: sys.exit('Explicit identity authorization required')
    context=subprocess.run(['kubectl','config','current-context'],text=True,capture_output=True,check=True).stdout.strip()
    assert context=='k0s-cluser'
    assert json.loads(call(['get','namespace','mansiflix','-o','json']))['status']['phase']=='Active'
    resume = sys.argv[sys.argv.index('--resume-name')+1] if '--resume-name' in sys.argv else None
    run=resume.removeprefix('mansiflix-video-lab-') if resume else uuid.uuid4().hex[:10];name='mansiflix-video-lab-'+run
    assert len(run)==10 and all(c in '0123456789abcdef' for c in run)
    evidence=Path('/tmp/mansiflix-video-lab-evidence');evidence.mkdir(exist_ok=True)
    records=[]
    # These new values live only in this process and the Kubernetes Secret payload.
    if not resume:
        records.append(create({'apiVersion':'v1','kind':'Secret','metadata':{'name':name,'namespace':'mansiflix','labels':{'app.kubernetes.io/name':'mansiflix-video-lab','mansiflix.local/video-lab-run':run}},'type':'Opaque','stringData':{'access-key':'video-lab-'+run,'secret-key':secrets.token_urlsafe(48)}}))
        records.append(create({'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':name+'-policy','namespace':'mansiflix'},'immutable':True,'data':{'policy.json':(root/'minio-policy.json').read_text()}}))
    else:
        for kind,suffix in [('secret',''),('configmap','-policy')]:
            assert call(['get',kind,name+suffix,'-o','name']).strip(), 'Missing partial resource'
        records=[{'kind':kind,'name':name+suffix,'uid':call(['get',kind,name+suffix,'-o','jsonpath={.metadata.uid}']).strip()} for kind,suffix in [('Secret',''),('ConfigMap','-policy')]]
    (evidence/'deployment-state.json').write_text(json.dumps({'run':run,'secretName':name,'resources':records},indent=2))
    script='''set +x
set -eu
umask 077
export MC_CONFIG_DIR=/tmp/mc MC_NO_COLOR=1
export MC_HOST_admin="http://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@minio:9000"
if mc --json admin user info admin "$LAB_ACCESS_KEY" >/tmp/check-user 2>&1; then
    printf 'FAIL identity already exists\\n'; exit 1
fi
grep -Eq '"Code"[[:space:]]*:[[:space:]]*"(XMinioAdminNoSuchUser|NoSuchUser)"' /tmp/check-user || { printf 'FAIL identity absence unconfirmed\\n'; exit 1; }
if mc --json admin policy info admin "$POLICY_NAME" >/tmp/check-policy 2>&1; then
    printf 'FAIL policy already exists\\n'; exit 1
fi
grep -Eq '"Code"[[:space:]]*:[[:space:]]*"(XMinioAdminNoSuchPolicy|NoSuchPolicy)"' /tmp/check-policy || { printf 'FAIL policy absence unconfirmed\\n'; exit 1; }
mc admin policy create admin "$POLICY_NAME" /opt/lab/policy.json >/tmp/response 2>&1 || { printf 'FAIL policy creation\\n'; exit 1; }
printf '%s\\n%s\\n' "$LAB_ACCESS_KEY" "$LAB_SECRET_KEY" | mc admin user add admin >/tmp/response 2>&1 || { printf 'FAIL identity creation\\n'; exit 1; }
mc admin policy attach admin "$POLICY_NAME" --user "$LAB_ACCESS_KEY" >/tmp/response 2>&1 || { printf 'FAIL policy attachment\\n'; exit 1; }
printf 'PASS isolated Video Lab identity configured\\n'
'''
    env=[{'name':var,'valueFrom':{'secretKeyRef':{'name':secret,'key':key}}} for var,secret,key in [('MINIO_ROOT_USER','mansiflix-minio-admin','MINIO_ROOT_USER'),('MINIO_ROOT_PASSWORD','mansiflix-minio-admin','MINIO_ROOT_PASSWORD'),('LAB_ACCESS_KEY',name,'access-key'),('LAB_SECRET_KEY',name,'secret-key')]]+[{'name':'POLICY_NAME','value':name}]
    job={'apiVersion':'batch/v1','kind':'Job','metadata':{'name':name+'-provision','namespace':'mansiflix'},'spec':{'backoffLimit':0,'activeDeadlineSeconds':120,'template':{'metadata':{'labels':{'app.kubernetes.io/part-of':'mansiflix','mansiflix.local/minio-access':'true','mansiflix.local/video-lab-run':run}},'spec':{'restartPolicy':'Never','automountServiceAccountToken':False,'nodeSelector':{'kubernetes.io/hostname':'nodo-5'},'securityContext':{'runAsNonRoot':True,'runAsUser':10001,'runAsGroup':10001,'fsGroup':10001,'seccompProfile':{'type':'RuntimeDefault'}},'containers':[{'name':'provision','image':'mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r1','imagePullPolicy':'Never','command':['/bin/sh','-ec'],'args':[script],'env':env,'resources':{'requests':{'cpu':'100m','memory':'128Mi'},'limits':{'cpu':'500m','memory':'512Mi'}},'securityContext':{'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'capabilities':{'drop':['ALL']}},'volumeMounts':[{'name':'temp','mountPath':'/tmp'},{'name':'policy','mountPath':'/opt/lab','readOnly':True}]}],'volumes':[{'name':'temp','emptyDir':{'sizeLimit':'64Mi'}},{'name':'policy','configMap':{'name':name+'-policy','defaultMode':292}}]}}}}
    # Manifest contains references only; safe to retain for review.
    (evidence/'provision-job.json').write_text(json.dumps(job,indent=2))
    records.append(create(job))
    (evidence/'deployment-state.json').write_text(json.dumps({'run':run,'secretName':name,'resources':records},indent=2))
