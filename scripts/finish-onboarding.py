"""Revoke an existing bootstrap endpoint once the worker is confirmed READY."""
import os
import pathlib
import urllib.request

state = pathlib.Path.home() / ".tidalbridge"
code = state / "onboarding.code"
if code.exists():
    request = urllib.request.Request("http://127.0.0.1:47833/" + code.read_text().strip() + "/ready", data=b"ready",method="POST")
    try:
        urllib.request.urlopen(request,timeout=5).close()
        print("Temporary onboarding endpoint closed")
    except OSError:
        print("Temporary onboarding endpoint is no longer listening")
(state / "ONBOARDING.txt").unlink(missing_ok=True)
