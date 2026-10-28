#!/usr/bin/env python3
"""Create a NEW synthetic demo through the real HTTP API; no S3 credentials.
Requires existing ffmpeg/ffprobe and a port-forward to Video Lab.
Writes only this run's synthetic upload/results via the app. No cleanup of S3.
"""
import argparse,hashlib,json,struct,subprocess,time,urllib.error,urllib.request,uuid
from pathlib import Path
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--url',default='http://127.0.0.1:8080');p.add_argument('--out',type=Path,required=True);args=p.parse_args();args.out.mkdir(parents=True,exist_ok=True)
fixture=args.out/'synthetic-1080.mp4'
subprocess.run(['ffmpeg','-hide_banner','-loglevel','error','-nostdin','-y','-f','lavfi','-i','testsrc2=size=1920x1080:rate=24','-f','lavfi','-i','sine=frequency=440:sample_rate=44100','-t','3','-c:v','libx264','-threads','1','-preset','ultrafast','-pix_fmt','yuv420p','-c:a','aac','-movflags','+faststart',str(fixture)],check=True)
boundary=uuid.uuid4().hex
body=(f'--{boundary}\r\nContent-Disposition: form-data; name="video"; filename="synthetic-1080.mp4"\r\nContent-Type: video/mp4\r\n\r\n'.encode()+fixture.read_bytes()+f'\r\n--{boundary}--\r\n'.encode())
r=urllib.request.Request(args.url+'/api/jobs',data=body,headers={'Content-Type':'multipart/form-data; boundary='+boundary})
with urllib.request.urlopen(r,timeout=120) as response:
 assert response.status==202;job=json.load(response)
assert job['originalSha256']==hashlib.sha256(fixture.read_bytes()).hexdigest()
end=time.monotonic()+180
while time.monotonic()<end:
 job=json.load(urllib.request.urlopen(args.url+'/api/jobs/'+job['id'],timeout=15))
 if job['status'] in ['completed','failed']:break
 time.sleep(1)
assert job['status']=='completed',job.get('error')
for result in job['results']:
 assert result['status']=='completed'
 with urllib.request.urlopen(args.url+'/api/jobs/'+job['id']+'/download/'+str(result['quality']),timeout=60) as response:data=response.read()
 assert hashlib.sha256(data).hexdigest()==result['sha256'] and len(data)==result['size']
 path=args.out/(str(result['quality'])+'.mp4');path.write_bytes(data)
 probe=json.loads(subprocess.run(['ffprobe','-v','error','-show_streams','-show_format','-of','json',str(path)],text=True,capture_output=True,check=True).stdout)
 assert any(s.get('codec_name')=='h264' and s.get('height')==result['quality'] for s in probe['streams'])
 assert any(s.get('codec_name')=='aac' for s in probe['streams'])
 atoms=[];pos=0
 while pos+8<=len(data):
  length,kind=struct.unpack('>I4s',data[pos:pos+8]);assert length>=8;atoms.append(kind);pos+=length
 assert atoms.index(b'moov')<atoms.index(b'mdat')
 print('PASS',result['quality'],result['size'],result['sha256'])
(args.out/'result.json').write_text(json.dumps(job,indent=2))
print('PASS real upload / encode / download, job',job['id'])
