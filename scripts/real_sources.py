"""Disposable real Prometheus/Loki semantic contract check. No production endpoints."""
import json, os, subprocess, time, urllib.request
from pathlib import Path
root=Path(__file__).resolve().parents[1]
work=root/'.local/real-sources'
work.mkdir(parents=True,exist_ok=True)
end=int(time.time())//300*300
lines=['# TYPE http_requests counter']
for status in ['200','500']:
    value=0.0
    for i in range(91):
        ts=end-90*60+i*60
        step=(12 if ts>end-15*60 else .1) if status=='500' else (88 if ts>end-15*60 else 99.9)
        value+=step
        lines.append(f'http_requests_total{{app="checkout",status="{status}"}} {value:.3f} {ts}')
lines.append('# EOF')
(work/'metrics.txt').write_text('\n'.join(lines)+'\n')
compose=['docker','compose','-f','compose.sources-test.yaml']
def ready(url):
    deadline=time.time()+90
    while time.time()<deadline:
        try:
            with urllib.request.urlopen(url,timeout=2) as response:
                if response.status==200:return
        except Exception:pass
        time.sleep(1)
    raise RuntimeError('Local source did not become ready: '+url)
try:
    subprocess.run(compose+['up','-d'],cwd=root,check=True)
    ready('http://127.0.0.1:19090/-/ready');ready('http://127.0.0.1:13100/ready')
    values=[]
    for i in range(90):
        ts=end-90*60+i*60+30
        line='level=info message=Request_completed'
        if ts>end-15*60:line='level=error message=Database_connection_acquisition_timeout password=secret-test-value'
        values.append([str(ts*1000000000),line])
    payload=json.dumps({'streams':[{'stream':{'app':'checkout'},'values':values}]}).encode()
    req=urllib.request.Request('http://127.0.0.1:13100/loki/api/v1/push',data=payload,headers={'Content-Type':'application/json'},method='POST')
    urllib.request.urlopen(req,timeout=10).close()
    env=os.environ|{'SHERLOCK_REAL_PROMETHEUS':'http://127.0.0.1:19090','SHERLOCK_REAL_LOKI':'http://127.0.0.1:13100','SHERLOCK_REAL_END':str(end)}
    subprocess.run(['go','test','-count=1','-v','-run','TestRealSources','./internal/connectors'],cwd=root,env=env,check=True)
finally:
    subprocess.run(compose+['down','-v'],cwd=root,check=True)
