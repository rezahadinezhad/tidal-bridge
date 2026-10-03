"""Exercise the built executable's actual newline-delimited MCP transport."""
import json
import pathlib
import subprocess

root = pathlib.Path(__file__).resolve().parents[1]
proc = subprocess.Popen([str(root / "bin/tidalbridge.exe"), "mcp"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
def send(value):
    proc.stdin.write(json.dumps(value) + "\n")
    proc.stdin.flush()
def call(identifier, method, params):
    send(dict(jsonrpc="2.0",id=identifier,method=method,params=params))
    while line := proc.stdout.readline():
        response = json.loads(line)
        if response.get("id") == identifier:
            assert "error" not in response, response
            return response["result"]
    raise AssertionError("MCP process closed unexpectedly")
try:
    init = call(1, "initialize", dict(protocolVersion="2025-03-26",capabilities={},clientInfo=dict(name="tidalbridge-smoke",version="1")))
    send(dict(jsonrpc="2.0",method="notifications/initialized"))
    tools = call(2,"tools/list",{})["tools"]
    assert len(tools) == 10
    result = call(3,"tools/call",dict(name="tidalbridge_status",arguments={}))
    assert not result.get("isError"), result
    print("PASS: MCP stdio initialize, 10 tools, live status")
finally:
    proc.stdin.close()
    proc.wait(timeout=10)
