import concurrent.futures, json, math, pathlib, subprocess, time, urllib.request

pid = int(subprocess.check_output(['systemctl','show','ddyidc','-p','MainPID','--value']))
def resource():
    fields = pathlib.Path(f'/proc/{pid}/status').read_text().splitlines()
    rss = int(next(x for x in fields if x.startswith('VmRSS:')).split()[1])
    connections = len(subprocess.check_output(['ss','-Hnt','dst','127.0.0.1:3306']).splitlines())
    return dict(rssKiB=rss, mysqlTCPConnections=connections)
before = resource()
def request():
    started=time.monotonic()
    try:
        with urllib.request.urlopen('http://127.0.0.1:8080/api/v1/products?page=1&pageSize=100',timeout=3) as r:
            raw=r.read(); body=json.loads(raw)
            valid=r.status==200 and body.get('code')==0 and isinstance(body.get('data',{}).get('items'),list)
            return dict(ms=(time.monotonic()-started)*1000,ok=valid,bytes=len(raw),count=len(body.get('data',{}).get('items',[])))
    except Exception:
        return dict(ms=(time.monotonic()-started)*1000,ok=False,bytes=0,count=0)
start=time.monotonic()
with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
    futures=[]
    for i in range(100):
        time.sleep(max(0,start+i/5-time.monotonic()))
        futures.append(pool.submit(request))
    rows=[f.result() for f in futures]
time.sleep(10)
after=resource()
times=sorted(r['ms'] for r in rows)
percentiles={f'p{n}Ms':round(times[math.ceil(len(times)*n/100)-1],2) for n in (50,95,99)}
with urllib.request.urlopen('http://127.0.0.1:8080/health/ready',timeout=3) as response:
    ready=response.status==200
passed=all(r['ok'] for r in rows) and percentiles['p95Ms']<=1000 and percentiles['p99Ms']<=2000 and after['rssKiB']-before['rssKiB']<=128*1024 and after['mysqlTCPConnections']-before['mysqlTCPConnections']<=5 and ready
result=dict(readOnly=True,total=100,ratePerSecond=5,maxWorkers=5,passed=passed,errors=sum(not r['ok'] for r in rows),latency=percentiles,before=before,after=after,catalogueCounts=sorted(set(r['count'] for r in rows)),bytes=sum(r['bytes'] for r in rows),ready=ready)
print(json.dumps(result,indent=2))
raise SystemExit(0 if passed else 1)
