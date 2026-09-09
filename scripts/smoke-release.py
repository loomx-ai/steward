#!/usr/bin/env python3
"""Run the shipped executable from an empty directory, including SQLite and UI."""

import json
from contextlib import closing
import os
from pathlib import Path
import re
import shutil
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def main():
    source = Path(sys.argv[1]).resolve()
    version = sys.argv[2]
    with tempfile.TemporaryDirectory(prefix="steward-release-") as directory:
        binary = str(Path(directory) / source.name)
        shutil.copy2(source, binary)
        env = {key: value for key, value in os.environ.items() if not key.startswith("STEWARD_")}
        env["STEWARD_AUTH_MODE"] = "local"
        if os.name == "nt":
            # A user's machine does not have MinGW's DLLs on PATH.
            system_root = os.environ["SystemRoot"]
            env["PATH"] = os.pathsep.join((str(Path(system_root) / "System32"), system_root))
        assert subprocess.check_output([binary, "--version"], cwd=directory, env=env, text=True).strip() == f"steward version {version}"
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        # Never route loopback smoke requests through a configured HTTP proxy.
        http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with tempfile.TemporaryFile(mode="w+") as log:
            process = subprocess.Popen([binary, "server", "start", "--addr", f"127.0.0.1:{port}"], cwd=directory, env=env, stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 60
                while True:
                    try:
                        with http.open(base + "/api/providers/catalog", timeout=2) as response:
                            assert response.status == 200
                            json.load(response)
                        break
                    except (urllib.error.URLError, TimeoutError):
                        if process.poll() is not None or time.monotonic() >= deadline:
                            raise AssertionError("server failed to start from an empty directory")
                        time.sleep(0.2)
                with http.open(base, timeout=5) as response:
                    html = response.read().decode()
                assert "Web assets are not embedded" not in html
                scripts = re.findall(r'<script[^>]+src="([^"]+)"', html)
                assert scripts, "the release has no bundled JavaScript"
                with http.open(base + scripts[0], timeout=5) as response:
                    assert response.status == 200 and len(response.read()) > 0
                data = Path(directory) / ".steward"
                assert (data / "credential-master-key").is_file()
                with closing(sqlite3.connect(data / "steward.db")) as db:
                    assert db.execute("SELECT MAX(version_id) FROM goose_db_version WHERE is_applied").fetchone()[0] > 0
                    assert db.execute("SELECT COUNT(*) FROM cloud_connections").fetchone()[0] == 0
                status = subprocess.check_output([binary, "server", "status"], cwd=directory, env=env, text=True)
                assert "status=running" in status and f"version={version}" in status, status
                subprocess.run([binary, "server", "stop"], cwd=directory, env=env, check=True)
                process.wait(timeout=15)
                status = subprocess.check_output([binary, "server", "status"], cwd=directory, env=env, text=True)
                assert "status=stopped" in status, status
            except BaseException:
                log.seek(0)
                print(log.read(), file=sys.stderr)
                raise
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()
    print("PASS: version, standalone SQLite migrations, embedded UI, server status and stop")


if __name__ == "__main__":
    main()
