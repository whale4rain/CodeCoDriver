#!/usr/bin/env python3
"""Small dependency-free document sidecar for CodeCoDriver."""

import json
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def parse_document(payload):
    content = str(payload.get("content", ""))
    filename = str(payload.get("filename", "document.txt"))
    lines = content.splitlines()
    chunks = []
    for start in range(0, len(lines), 80):
        chunk_lines = lines[start:start + 80]
        chunks.append({
            "text": "\n".join(chunk_lines),
            "start_line": start + 1,
            "end_line": start + len(chunk_lines),
            "tokens": re.findall(r"[A-Za-z0-9_]{2,}", " ".join(chunk_lines)),
        })
    return {
        "filename": filename,
        "format": filename.rsplit(".", 1)[-1].lower() if "." in filename else "text",
        "line_count": len(lines),
        "chunks": chunks,
    }


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self._write(200, {"status": "ok"})
            return
        self._write(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/parse":
            self._write(404, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            payload = json.loads(self.rfile.read(length))
            self._write(200, parse_document(payload))
        except (ValueError, json.JSONDecodeError) as exc:
            self._write(400, {"error": str(exc)})

    def _write(self, status, payload):
        data = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *_):
        return


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8091), Handler).serve_forever()
