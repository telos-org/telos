"""Serve read-only Cloud fixtures for the CLI smoke test."""

import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
import sys


class SmokeCloud(BaseHTTPRequestHandler):
    def do_GET(self):
        responses = {
            "/api/account/bootstrap": {
                "personal_org_id": "org_smoke",
                "organizations": [
                    {"id": "org_smoke", "default_publish_scope": "smoke"}
                ],
            },
            "/api/deployments": {"deployments": []},
        }
        response = responses.get(self.path)
        self.send_response(200 if response is not None else 404)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(response or {"detail": "not found"}).encode())

    def reject_write(self):
        (self.server.fixture_dir / "cloud-writes").touch()
        self.send_error(405, "The smoke fixture accepts only reads")

    do_POST = reject_write
    do_PUT = reject_write
    do_PATCH = reject_write
    do_DELETE = reject_write


if __name__ == "__main__":
    with HTTPServer(("127.0.0.1", 0), SmokeCloud) as server:
        server.fixture_dir = Path(sys.argv[1])
        endpoint = f"http://127.0.0.1:{server.server_port}"
        (server.fixture_dir / "config.yaml").write_text(
            f"api_endpoint: {endpoint}\nauth_token: smoke-token\n"
        )
        server.serve_forever()
