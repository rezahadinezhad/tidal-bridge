"""Update an already paired Termux worker through its existing authenticated channel."""
import argparse
import json
import os
import pathlib
import time
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("--device", required=True)
parser.add_argument("--data-dir", type=pathlib.Path, default=pathlib.Path.home() / ".tidalbridge")
args = parser.parse_args()
root = pathlib.Path(__file__).resolve().parents[1]
token = (args.data_dir / "host.token").read_text().strip()
bind = "127.0.0.1:47831"
for line in (args.data_dir / "config.yaml").read_text().splitlines():
    if line.startswith("bind:"):
        bind = line.split(":", 1)[1].strip().strip('"')
def api(path, body=None):
    request = urllib.request.Request("http://" + bind + "/v1/" + path, data=None if body is None else json.dumps(body).encode(), headers={"Authorization":"Bearer " + token,"Content-Type":"application/json"})
    return json.load(urllib.request.urlopen(request, timeout=30))
node = next(n for n in api("status")["workers"] if n["id"] == args.device)
if node["profile"]["capabilities"]["simulated"] or node["active_jobs"]:
    raise SystemExit("Update requires an idle, real Termux worker")
code = """import os,pathlib,shutil,subprocess
prefix=os.environ['PREFIX']; home=pathlib.Path(os.environ['HOME'])
shutil.copyfile('worker.py',home/'tidalbridge/worker/worker.py')
env=dict(os.environ)
restart="import os,time;time.sleep(3);os.execv('/system/bin/linker64',['linker64',os.environ['PREFIX']+'/bin/sv','restart',os.environ['PREFIX']+'/var/service/tidalbridge'])"
with (home/'tidalbridge/logs/update.log').open('w') as log:
 subprocess.Popen(['/system/bin/linker64',prefix+'/bin/python','-c',restart],env=env,stdin=subprocess.DEVNULL,stdout=log,stderr=log,start_new_session=True)
print('Worker update staged; restart scheduled')
"""
job = api("jobs", dict(argv=["python", "-c", code], workspace=str(root / "apps/android-worker"),timeout_seconds=60,policy=dict(force_remote=True,device_id=args.device,local_fallback=False)))
deadline = time.monotonic() + 90
while time.monotonic() < deadline:
    job = api("jobs/" + job["id"])
    if job.get("finished"):
        if job["state"] != "COMPLETED":
            raise SystemExit(job.get("error") or job.get("stderr") or job["state"])
        print(job.get("stdout", "").strip())
        break
    time.sleep(0.3)
else:
    raise SystemExit("Update staging timed out")
expected = next(line.split('"')[1] for line in (root / "apps/android-worker/worker.py").read_text().splitlines() if line.startswith("VERSION ="))
while time.monotonic() < deadline:
    time.sleep(3)
    try:
        api("refresh", {})
        nodes = api("status")["workers"]
    except OSError:
        continue
    if any(n["id"] == args.device and n["state"] == "READY" and n["profile"]["capabilities"]["worker_version"] == expected for n in nodes):
        print("Worker READY on version", expected)
        break
else:
    raise SystemExit("Worker did not confirm its new version; inspect ~/tidalbridge/logs/update.log in Termux")
