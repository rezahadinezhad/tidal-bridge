"""Comparative execution and daemon-overhead evidence via the actual host API."""
import argparse
import json
import pathlib
import statistics
import subprocess
import time
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("--data-dir", type=pathlib.Path, required=True)
parser.add_argument("--device")
parser.add_argument("--seconds", type=int, default=30)
parser.add_argument("--dashboard-open", action="store_true", help="Label the UI state accurately; does not control the browser")
parser.add_argument("--output", type=pathlib.Path, default=pathlib.Path("benchmark-results/benchmark.json"))
args = parser.parse_args()
binary = pathlib.Path(__file__).resolve().parents[1] / "bin/tidalbridge.exe"
status = json.loads(subprocess.check_output([str(binary), "--data-dir", str(args.data_dir), "status"], text=True))
# Config is read through the host CLI, avoiding a Python YAML dependency.
bind = "127.0.0.1:47831"
for line in (args.data_dir / "config.yaml").read_text().splitlines():
    if line.startswith("bind:"):
        bind = line.split(":", 1)[1].strip().strip('"')
token = (args.data_dir / "host.token").read_text().strip()
def api(path, body=None):
    request = urllib.request.Request("http://" + bind + "/v1/" + path, data=None if body is None else json.dumps(body).encode(), headers={"Authorization":"Bearer " + token,"Content-Type":"application/json"})
    return json.load(urllib.request.urlopen(request))
samples = []
start = time.monotonic()
while time.monotonic() - start < args.seconds:
    samples.append(api("status")["host"])
    time.sleep(min(5, args.seconds))
report = {"kind":"measured daemon idle sampling","duration_seconds":args.seconds,"dashboard_open":args.dashboard_open,"rss_average_mb":statistics.mean(s["daemon_rss_mb"] for s in samples),"rss_peak_mb":max(s["daemon_rss_mb"] for s in samples),"daemon_cpu_average_percent":statistics.mean(s["daemon_cpu_percent"] for s in samples),"samples":samples,"workers":status["workers"],"comparisons":[]}
root = pathlib.Path(__file__).resolve().parents[1]
memory = """import os,pathlib,json,time
block=bytearray(256*1024*1024)
if os.name=='nt':
 import ctypes
 class Counters(ctypes.Structure):
  _fields_=[('cb',ctypes.c_ulong),('faults',ctypes.c_ulong)]+[(n,ctypes.c_size_t) for n in ('peak','working','peakPaged','paged','peakNonpaged','nonpaged','page','peakPage')]
 from ctypes import wintypes
 ctypes.windll.kernel32.GetCurrentProcess.restype=wintypes.HANDLE
 ctypes.windll.psapi.GetProcessMemoryInfo.argtypes=[wintypes.HANDLE,ctypes.POINTER(Counters),wintypes.DWORD]
 c=Counters();c.cb=ctypes.sizeof(c)
 if not ctypes.windll.psapi.GetProcessMemoryInfo(ctypes.windll.kernel32.GetCurrentProcess(),ctypes.byref(c),c.cb): raise ctypes.WinError()
 rss=c.working/1048576
else:
 import re
 rss=int(re.search(r'^VmRSS:\\s+(\\d+)',pathlib.Path('/proc/self/status').read_text(),re.M)[1])/1024
print(json.dumps({'allocated_mb':256,'process_rss_mb':rss}),flush=True);time.sleep(3)
"""
workloads = {"python_cpu":["python","-c","print(sum(i*i for i in range(4000000)))"],"python_tests":["python","-m","unittest","discover","-v"],"node_cpu":["node","-e","let s=0;for(let i=0;i<8000000;i++)s+=i;console.log(s)"],"compression":["python","-c","import zlib;print(len(zlib.compress(b'tidalbridge'*1000000)))"],"json_parse":["python","-c","import json;print(len(json.loads(json.dumps(list(range(500000))))))"],"memory_256mb":["python","-c",memory]}
for name, argv in workloads.items():
    for target in ["LOCAL"] + ([args.device] if args.device else []):
        job = api("jobs",dict(argv=argv,workspace=str(root / "tests/fixtures/small-project") if name=="python_tests" else "", timeout_seconds=60,requirements=dict(ram_mb=512 if name=="memory_256mb" else 128),policy=dict(force_local=target=="LOCAL",force_remote=target!="LOCAL",device_id="" if target=="LOCAL" else target,idempotent=True,retryable=True,local_fallback=False)))
        while not (job := api("jobs/" + job["id"])).get("finished"):
            time.sleep(0.25)
        report["comparisons"].append({"workload":name,"target":target,"state":job["state"],"attempts":job["attempts"],"stdout":job.get("stdout"),"stderr":job.get("stderr"),"error":job.get("error"),"host_relief":"Process placement and root RSS measured for memory workload; no net workstation CPU/energy relief claim. Simulated workers provide no physical relief."})
args.output.parent.mkdir(parents=True,exist_ok=True)
args.output.write_text(json.dumps(report,indent=2))
print(json.dumps({key:report[key] for key in ("duration_seconds","rss_average_mb","rss_peak_mb","daemon_cpu_average_percent")},indent=2))
print("Saved",args.output.resolve())
