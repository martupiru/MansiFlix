#!/usr/bin/env python3
"""Deploy new isolated Video Lab resources. No existing resource is updated.

The existing FFmpeg image is already on nodo-5. With no registry/SSH available,
the static S3/API binary is delivered as immutable ConfigMap chunks, verified
by SHA-256 in an initContainer. This is a bounded lab delivery mechanism.
"""
import base64,gzip,hashlib,json,os,subprocess,sys
from pathlib import Path
os.environ['KUBECONFIG']='/Users/maurosorbello/Documents/Middleware/kubeconfig'
os.umask(0o077)
evidence=Path('/tmp/mansiflix-video-lab-evidence')
state=json.loads((evidence/'deployment-state.json').read_text())
run=state['run'];name='mansiflix-video-lab-'+run
base=['kubectl','--context=k0s-cluser','-n','mansiflix','--request-timeout=20s']
def call(args,payload=None):
 with (evidence/'commands.jsonl').open('a') as f:f.write(json.dumps(base+args)+'\n')
 r=subprocess.run(base+args,input=payload,capture_output=True,text=True,timeout=30)
 if r.returncode:sys.exit('Kubernetes operation failed: '+' '.join(args[:3]))
 return r.stdout

def create(obj):
 k,n=obj['kind'],obj['metadata']['name']
 if call(['get',k,n,'--ignore-not-found','-o','name']).strip():sys.exit('Refusing existing resource '+n)
 r=json.loads(call(['create','-f','-','-o','json'],json.dumps(obj)))
 state['resources'].append({'kind':k,'name':n,'uid':r['metadata']['uid']})
 (evidence/'deployment-state.json').write_text(json.dumps(state,indent=2))
 print('CREATED',k,n,flush=True)

if __name__=='__main__':
 assert subprocess.run(['kubectl','config','current-context'],capture_output=True,text=True).stdout.strip()=='k0s-cluser'
 assert json.loads(call(['get','namespace','mansiflix','-o','json']))['status']['phase']=='Active'
 binary=Path('/tmp/mansiflix-video-lab').read_bytes();packed=gzip.compress(binary);chunk=650000
 digest=hashlib.sha256(binary).hexdigest();chunks=[packed[i:i+chunk] for i in range(0,len(packed),chunk)]
 volumes=[];mounts=[];manifests=[]
 for i,data in enumerate(chunks):
  part=f'part-{i:02}';cmname=name+'-'+part
  cm={'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':cmname,'namespace':'mansiflix','labels':{'app.kubernetes.io/name':'mansiflix-video-lab','mansiflix.local/video-lab-run':run}},'immutable':True,'binaryData':{'chunk.gz':base64.b64encode(data).decode()}}
  manifests.append(cm);volumes.append({'name':part,'configMap':{'name':cmname,'defaultMode':292}});mounts.append({'name':part,'mountPath':'/artifacts/'+part,'readOnly':True})
 security={'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'capabilities':{'drop':['ALL']}}
 resource={'requests':{'cpu':'200m','memory':'256Mi','ephemeral-storage':'256Mi'},'limits':{'cpu':'1','memory':'512Mi','ephemeral-storage':'2Gi'}}
 volumes += [{'name':'runtime','emptyDir':{'sizeLimit':'32Mi'}},{'name':'work','emptyDir':{'sizeLimit':'1Gi'}},{'name':'temp','emptyDir':{'sizeLimit':'64Mi'}}]
 script="set -eu; umask 077; cat /artifacts/part-*/chunk.gz | gzip -d >/runtime/video-lab; printf '%s  /runtime/video-lab\\n' '"+digest+"' | sha256sum -c -; chmod 700 /runtime/video-lab"
 pod={'automountServiceAccountToken':False,'nodeSelector':{'kubernetes.io/hostname':'nodo-5'},'securityContext':{'runAsNonRoot':True,'runAsUser':10001,'runAsGroup':10001,'fsGroup':10001,'seccompProfile':{'type':'RuntimeDefault'}},'initContainers':[{'name':'install-app','image':'mansiflix/video-worker:demo-88bb1d91e247','imagePullPolicy':'Never','command':['/bin/sh','-ec'],'args':[script],'resources':{'requests':{'cpu':'100m','memory':'32Mi'},'limits':{'cpu':'500m','memory':'128Mi'}},'securityContext':security,'volumeMounts':mounts+[{'name':'runtime','mountPath':'/runtime'}]}],'containers':[{'name':'web','image':'mansiflix/video-worker:demo-88bb1d91e247','imagePullPolicy':'Never','command':['/runtime/video-lab'],'env':[{'name':'S3_ENDPOINT','value':'http://minio.mansiflix.svc.cluster.local:9000'},{'name':'WORK_DIR','value':'/work'},{'name':'GOMEMLIMIT','value':'96MiB'},{'name':'S3_ACCESS_KEY','valueFrom':{'secretKeyRef':{'name':state['secretName'],'key':'access-key'}}},{'name':'S3_SECRET_KEY','valueFrom':{'secretKeyRef':{'name':state['secretName'],'key':'secret-key'}}}],'ports':[{'name':'http','containerPort':8080}],'startupProbe':{'httpGet':{'path':'/healthz','port':'http'},'periodSeconds':2,'failureThreshold':30},'readinessProbe':{'httpGet':{'path':'/healthz','port':'http'},'periodSeconds':5},'livenessProbe':{'httpGet':{'path':'/healthz','port':'http'},'periodSeconds':15},'securityContext':security,'resources':resource,'volumeMounts':[{'name':'runtime','mountPath':'/runtime','readOnly':True},{'name':'work','mountPath':'/work'},{'name':'temp','mountPath':'/tmp'}]}],'volumes':volumes}
 labels={'app.kubernetes.io/name':'mansiflix-video-lab','mansiflix.local/video-lab-run':run}
 deployment={'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':name,'namespace':'mansiflix','labels':labels},'spec':{'replicas':1,'strategy':{'type':'Recreate'},'selector':{'matchLabels':labels},'template':{'metadata':{'labels':{**labels,'app.kubernetes.io/part-of':'mansiflix','mansiflix.local/minio-access':'true'}},'spec':pod}}}
 service={'apiVersion':'v1','kind':'Service','metadata':{'name':name,'namespace':'mansiflix'},'spec':{'type':'ClusterIP','selector':labels,'ports':[{'name':'http','port':8080,'targetPort':'http'}]}}
 manifests += [deployment,service]
 (evidence/'manifests.json').write_text(json.dumps({'apiVersion':'v1','kind':'List','items':manifests},indent=2))
 state['appName']=name;state['binarySHA256']=digest
 for obj in manifests:create(obj)
 print('PORT_FORWARD: kubectl --context=k0s-cluser -n mansiflix port-forward service/'+name+' 8080:8080')
