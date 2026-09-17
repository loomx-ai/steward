#!/usr/bin/env python3
"""Run the opt-in mockgcp verification tests against Google's pinned mocks.

Checks out the pinned Config Connector revision into a temporary directory,
builds each retained harness from `providers/gcp/fixtures/*/testdata/mockgcp`
inside that module, and runs the Steward tests that need it. Each test gets a
freshly started harness so no fixture state leaks between tests.

Requires Go and network access for the checkout and module downloads. Uses only
the standard library. Run from anywhere:

    python3 scripts/run-mockgcp-tests.py [--keep] [--checkout DIR] [TEST ...]
"""

import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
COMMIT = "673a61419de1b8e4f7d26070ce20dde2daa61da8"
REPOSITORY = "https://github.com/GoogleCloudPlatform/k8s-config-connector.git"
HARNESSES = {
    "compute": {
        "main": "providers/gcp/fixtures/firewall-policy/testdata/mockgcp/main.go",
        "env": ["STEWARD_FIREWALL_MOCKGCP_URL", "STEWARD_FUTURE_RESERVATION_MOCKGCP_URL"],
        "tests": ["TestFirewallIndependentMockGCP", "TestFutureReservationIndependentMockGCP"],
    },
    "monitoring": {
        "main": "providers/gcp/fixtures/metrics-scope/testdata/mockgcp/main.go",
        "env": [
            "STEWARD_ALERT_POLICY_MOCKGCP_URL",
            "STEWARD_METRICS_SCOPE_MOCKGCP_URL",
            "STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL",
            "STEWARD_UPTIME_MOCKGCP_URL",
        ],
        "tests": [
            "TestAlertPolicyIndependentMockGCP",
            "TestBillingBudgetIndependentMockGCP",
            "TestBillingBudgetDeleteIndependentMockGCP",
            "TestBillingBudgetInventoryIndependentMockGCP",
            "TestBillingBudgetProjectIndependentMockGCP",
            "TestLoggingRoutingIndependentMockGCP",
            "TestMetricsScopeIndependentMockGCP",
            "TestMonitoringDashboardIndependentMockGCP",
            "TestMonitoringDashboardPolicyIndependentMockGCP",
            "TestMonitoringDashboardUptimeIndependentMockGCP",
            "TestMonitoringGroupIndependentMockGCP",
            "TestMonitoringGroupDeleteIndependentMockGCP",
            "TestNotificationChannelIndependentMockGCP",
            "TestNotificationChannelDeleteIndependentMockGCP",
            "TestUptimeIndependentMockGCP",
        ],
    },
    "identity": {
        "main": "providers/gcp/fixtures/identity-groups/testdata/mockgcp/main.go",
        "env": ["STEWARD_IDENTITY_MOCKGCP_URL"],
        "tests": ["TestIdentityGroupsIndependentMockGCP"],
    },
    "deployment": {
        "main": "providers/gcp/fixtures/deployment-group/testdata/mockgcp/main.go",
        "env": ["STEWARD_DEPLOYMENT_GROUP_MOCKGCP_URL"],
        "tests": ["TestDeploymentGroupIndependentMockGCP"],
    },
}


def checkout(directory):
    mockgcp = directory / "mockgcp"
    if mockgcp.exists():
        return
    # The mockgcp module replaces the repository root and the vendored
    # terraform provider, so the harnesses need the whole checkout.
    run = lambda *args: subprocess.run(args, cwd=directory, check=True)
    run("git", "init", "-q")
    run("git", "remote", "add", "origin", REPOSITORY)
    run("git", "fetch", "--depth", "1", "origin", COMMIT)
    run("git", "checkout", "-q", "FETCH_HEAD")


def build(directory, name, harness):
    module = directory / "mockgcp"
    command = module / "cmd" / f"steward-{name}-harness"
    command.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(ROOT / harness["main"], command / "main.go")
    binary = directory / f"steward-{name}-harness"
    subprocess.run(["go", "build", "-o", str(binary), f"./cmd/steward-{name}-harness"],
                   cwd=module, check=True, env={**__import__("os").environ, "GOWORK": "off"})
    return binary


def serve(binary, log):
    process = subprocess.Popen([str(binary)], stdout=log, stderr=subprocess.STDOUT)
    deadline = time.time() + 60
    while time.time() < deadline:
        if process.poll() is not None:
            raise SystemExit(f"{binary.name} exited before printing its origin")
        text = Path(log.name).read_text().splitlines()
        if text and text[0].startswith("http://127.0.0.1:"):
            return process, text[0].strip()
        time.sleep(0.2)
    process.kill()
    raise SystemExit(f"{binary.name} did not print a loopback origin")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("tests", nargs="*", help="run only these test names")
    parser.add_argument("--checkout", help="reuse this pinned checkout directory")
    parser.add_argument("--keep", action="store_true", help="keep the checkout")
    arguments = parser.parse_args()
    directory = Path(arguments.checkout) if arguments.checkout else Path(tempfile.mkdtemp(prefix="mockgcp-"))
    directory.mkdir(parents=True, exist_ok=True)
    failures, passed = [], 0
    try:
        checkout(directory)
        for name, harness in HARNESSES.items():
            tests = [test for test in harness["tests"] if not arguments.tests or test in arguments.tests]
            if not tests:
                continue
            binary = build(directory, name, harness)
            for test in tests:
                # A fresh harness per test keeps fixture identities unique.
                with open(directory / f"{name}.log", "w") as log:
                    process, origin = serve(binary, log)
                    try:
                        environment = {**__import__("os").environ, **{key: origin for key in harness["env"]}}
                        result = subprocess.run(
                            ["go", "test", "./providers/gcp", "-count=1", "-run", f"^{test}$", "-v"],
                            cwd=ROOT, env=environment, capture_output=True, text=True)
                    finally:
                        process.terminate()
                        process.wait(timeout=30)
                if result.returncode != 0 or "--- PASS" not in result.stdout:
                    failures.append(test)
                    print(result.stdout[-4000:], file=sys.stderr)
                else:
                    passed += 1
                print(f"{'ok  ' if test not in failures else 'FAIL'} {test}", flush=True)
    finally:
        if not arguments.keep and not arguments.checkout:
            shutil.rmtree(directory, ignore_errors=True)
    print(json.dumps({"passed": passed, "failed": failures}))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
