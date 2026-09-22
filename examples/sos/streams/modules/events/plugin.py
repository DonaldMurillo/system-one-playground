#!/usr/bin/env python3
"""Offline sos-plugin/1 stream fixture used by the runnable examples."""

import json
import sys


def receive():
    line = sys.stdin.readline()
    if not line:
        raise EOFError
    return json.loads(line)


def send(message):
    print(json.dumps(message, separators=(",", ":")), flush=True)


def notification(method, params):
    send({"jsonrpc": "2.0", "method": method, "params": params})


def run_stream(request):
    params = request["params"]
    action = params["action"]
    count = params.get("arguments", {}).get("count")
    stream_id = "stream-{}".format(request["id"])
    send({"jsonrpc": "2.0", "id": request["id"], "result": {"streamId": stream_id, "itemType": "Event"}})
    sequence = 0
    credit = params["credit"]
    while count is None or sequence < count:
        if credit == 0:
            control = receive()
            control_params = control.get("params", {})
            if control.get("method") == "stream.cancel" and control_params.get("streamId") == stream_id:
                notification("stream.end", {"streamId": stream_id, "lastSequence": sequence - 1})
                return
            if control.get("method") != "stream.credit" or control_params.get("streamId") != stream_id:
                raise RuntimeError("unexpected stream control message")
            credit += control_params["credit"]
        notification("stream.item", {"streamId": stream_id, "sequence": sequence, "value": {"sequence": sequence, "message": "event {}".format(sequence)}})
        sequence += 1
        credit -= 1
    if action == "failing":
        notification("stream.error", {"streamId": stream_id, "failure": {"kind": "ConnectionLost", "message": "example connection lost", "retryable": True, "payload": {"after": sequence}}})
    else:
        notification("stream.end", {"streamId": stream_id, "lastSequence": sequence - 1})


def main():
    while True:
        try:
            request = receive()
        except EOFError:
            return
        method = request.get("method")
        if method == "initialize":
            params = request["params"]
            send({"jsonrpc": "2.0", "id": request["id"], "result": {"protocol": params["protocol"], "module": params["module"], "version": params["version"], "definitionDigest": params["definitionDigest"]}})
        elif method == "stream.open":
            run_stream(request)
        elif method == "stream.credit":
            # A finite producer may have sent its terminal notification before
            # the host learned that the last delivered item was last.
            continue
        else:
            send({"jsonrpc": "2.0", "id": request.get("id"), "error": {"code": -32601, "message": "method not found"}})


if __name__ == "__main__":
    main()
