#!/usr/bin/env python3
import json
import os
import sys

for line in sys.stdin:
    request = json.loads(line)
    method = request.get("method")
    if method == "initialize":
        params = request["params"]
        result = {
            "protocol": params["protocol"],
            "module": params["module"],
            "version": params["version"],
            "definitionDigest": params["definitionDigest"],
            "pid": os.getpid(),
        }
    elif method == "invoke":
        args = request["params"]["arguments"]
        if request["params"]["action"] == "reject":
            print(json.dumps({"jsonrpc":"2.0","id":request["id"],"error":{"code":-32010,"message":"rejected","data":{"kind":"Rejected","retryable":False,"payload":{"value":args["value"]}}}}), flush=True)
            continue
        result = {"value": os.getpid()}
    elif method == "shutdown":
        result = None
    else:
        continue
    print(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}), flush=True)
    if method == "shutdown":
        break
