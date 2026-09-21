#!/usr/bin/env python3
"""Small persistent sos-plugin/1 process used by the repository examples."""

import json
import os
import sys
import time


def reply(request, result=None, error=None):
    response = {"jsonrpc": "2.0", "id": request["id"]}
    response["error" if error else "result"] = error if error else result
    print(json.dumps(response, separators=(",", ":")), flush=True)


for line in sys.stdin:
    request = json.loads(line)
    method = request.get("method")
    if method == "initialize":
        params = request["params"]
        reply(request, {
            "protocol": params["protocol"],
            "module": params["module"],
            "version": params["version"],
            "definitionDigest": params["definitionDigest"],
        })
    elif method == "invoke":
        params = request["params"]
        arguments = params["arguments"]
        if params["action"] == "inspect":
            if not os.environ.get("SOS_EXAMPLE_PLUGIN_TOKEN"):
                reply(request, error={
                    "code": -32010,
                    "message": "profile request rejected",
                    "data": {
                        "kind": "Rejected",
                        "retryable": False,
                        "payload": {"reason": "the approved token is missing"},
                    },
                })
            else:
                reply(request, {"value": {
                    "name": arguments["name"],
                    "age": arguments["age"],
                    "verified": True,
                }})
        elif params["action"] == "reject":
            reply(request, error={
                "code": -32010,
                "message": "profile request rejected",
                "data": {
                    "kind": "Rejected",
                    "retryable": False,
                    "payload": {"reason": arguments["reason"]},
                },
            })
        elif params["action"] == "wait":
            time.sleep(arguments["milliseconds"] / 1000)
            reply(request, {"value": "finished"})
    elif method == "shutdown":
        reply(request)
        break
    elif method == "cancel":
        # The host deadline remains authoritative and kills an unresponsive tree.
        continue
