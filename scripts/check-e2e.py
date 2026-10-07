# Copyright Pydantic, Inc. 2025, 2026
# SPDX-License-Identifier: MPL-2.0

"""Run the compiled provider through Terraform against a local fault-injecting API."""

import copy
import http.server
import json
import os
import subprocess
import tempfile
import threading
from pathlib import Path
from urllib.parse import parse_qs

PROJECT_ID = "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"
PROJECT_PATH = f"/api/v1/projects/{PROJECT_ID}/"
CHANNEL_PATH = "/api/v1/channels/"
ORG_PATH = "/api/v1/instance/organizations/"
DEFINITION = {"kind": "Dashboard", "metadata": {}, "spec": {}}
WEBHOOK = (
    'config {\ntype = "webhook"\nurl = "https://example.com/hook"\nformat = "auto"\n}'
)


class API(http.server.ThreadingHTTPServer):
    def __init__(self):
        super().__init__(("127.0.0.1", 0), Handler)
        self.requests = []
        self.objects = {
            "/api/v1/projects/": {
                PROJECT_ID: {
                    "id": PROJECT_ID,
                    "project_name": "my-project",
                    "organization_name": "acme",
                    "description": None,
                    "visibility": "public",
                }
            },
            CHANNEL_PATH: {},
            ORG_PATH: {},
            PROJECT_PATH + "dashboards/": {},
            PROJECT_PATH + "read-tokens/": {},
            PROJECT_PATH + "write-tokens/": {},
        }
        self.fail_followup = False
        self.create_failure = None
        self.created = []

    def request(self, method, path, payload):
        self.requests.append((method, path, payload))
        if path == "/api/oauth/token":
            if self.fail_followup:
                return 400, {
                    "error": "invalid_scope",
                    "error_description": "mock rejection",
                }
            return 200, {"access_token": "mock-org-token", "expires_in": 900}
        if path == "/api/v1/organization/":
            objects = self.objects[ORG_PATH]
            item = next(iter(objects.values()))
            if method == "PUT":
                item.update(payload)
            elif method == "DELETE":
                objects.clear()
                return 204, None
            return 200, item
        if method == "POST":
            self.created.append(path)
            if self.create_failure:
                return self.create_failure, {"detail": "response lost after creation"}
            objects = self.objects[path]
            if path == "/api/v1/projects/":
                item = {
                    "id": PROJECT_ID,
                    "organization_name": "acme",
                    "description": None,
                    "visibility": "public",
                    **payload,
                }
            elif path == CHANNEL_PATH:
                config = payload["config"].copy()
                if config["type"] == "slack-integration":
                    config.setdefault("include_agent_prompt", True)
                item = {"id": "channel-1", "active": True, **payload, "config": config}
            elif path == ORG_PATH:
                item = {"id": "org-1", **payload}
            elif path.endswith("dashboards/"):
                item = {
                    "id": "dashboard-1",
                    "project_id": PROJECT_ID,
                    "dashboard_name": payload["name"],
                    "dashboard_slug": payload["slug"],
                    "definition": payload["definition"],
                }
            else:
                item = {
                    "id": "token-1",
                    "project_id": PROJECT_ID,
                    "token": "mock-created-token",
                    "token_prefix": "mock",
                    "description": "Created by Public API",
                    "expires_at": None,
                    **payload,
                }
            objects[item["id"]] = item
            return 201, self.response(item, path)
        for collection, objects in self.objects.items():
            if path == collection and method == "GET":
                return 200, [
                    self.response(item, collection, hide_token=True)
                    for item in objects.values()
                ]
            if path.startswith(collection) and path[len(collection) :].count("/") == 1:
                item_id = path[len(collection) :].strip("/")
                if item_id not in objects:
                    return 404, {"detail": "not found"}
                item = objects[item_id]
                if method == "DELETE":
                    del objects[item_id]
                    return 204, None
                if method == "PUT":
                    if self.fail_followup:
                        return 400, {"detail": "mock rejection"}
                    if collection.endswith("dashboards/"):
                        if "name" in payload:
                            item["dashboard_name"] = payload["name"]
                            item["definition"]["metadata"]["name"] = payload["name"]
                        if "definition" in payload:
                            item["definition"] = payload["definition"]
                    else:
                        if (
                            collection == CHANNEL_PATH
                            and "config" in payload
                            and payload["config"]["type"] == "slack-integration"
                        ):
                            payload["config"].setdefault("include_agent_prompt", True)
                        item.update(payload)
                        if collection == "/api/v1/projects/":
                            if item["description"] == "":
                                item["description"] = None
                            if item["visibility"] is None:
                                item["visibility"] = "public"
                if collection.endswith("dashboards/") and method == "GET":
                    return 200, {"dashboard": item["definition"]}
                return 200, self.response(item, collection)
        return 404, {"detail": "unexpected route"}

    @staticmethod
    def response(item, collection, hide_token=False):
        item = copy.deepcopy(item)
        if hide_token:
            item.pop("token", None)
        if collection == CHANNEL_PATH and item["config"]["type"] == "webhook":
            item["config"]["url"] = "https://example.com/**********"
        return item


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def handle_request(self):
        if self.headers.get("Content-Length"):
            raw = self.rfile.read(int(self.headers["Content-Length"]))
        elif self.headers.get("Transfer-Encoding") == "chunked":
            chunks = []
            while True:
                size = int(self.rfile.readline().split(b";")[0], 16)
                if not size:
                    self.rfile.readline()
                    break
                chunks.append(self.rfile.read(size))
                self.rfile.read(2)
            raw = b"".join(chunks)
        else:
            raw = b""
        payload = (
            parse_qs(raw.decode())
            if self.headers.get("Content-Type") == "application/x-www-form-urlencoded"
            else json.loads(raw)
            if raw
            else None
        )
        status, payload = self.server.request(self.command, self.path, payload)
        if status == "disconnect":
            self.close_connection = True
            return
        raw = json.dumps(payload).encode() if payload is not None else b""
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("Retry-After", "0")
        self.end_headers()
        self.wfile.write(raw)

    do_GET = do_POST = do_PUT = do_DELETE = handle_request


class Terraform:
    def __init__(self, root, api, cli_config):
        self.root = root
        root.mkdir()
        self.api = api
        self.env = {
            **os.environ,
            "TF_CLI_CONFIG_FILE": str(cli_config),
            "TF_IN_AUTOMATION": "1",
            "TF_INPUT": "0",
            "CHECKPOINT_DISABLE": "1",
        }

    def config(self, kind, body):
        self.kind = kind
        provider = f'provider "logfire" {{\nbase_url = "http://127.0.0.1:{self.api.server_port}"\napi_key = "mock-provider-token"\n}}'
        (self.root / "main.tf").write_text(
            'terraform {\nrequired_providers {\nlogfire = { source = "pydantic/logfire" }\n}\n}\n'
            + provider
            + f'\nresource "logfire_{kind}" "test" {{\n{body}\n}}\n'
        )

    def run(self, *args, code=0, error=None):
        command = (
            ["terraform", "state", args[1], "-no-color", *args[2:]]
            if args[0] == "state"
            else ["terraform", args[0], "-no-color", *args[1:]]
        )
        result = subprocess.run(
            command,
            cwd=self.root,
            env=self.env,
            text=True,
            capture_output=True,
            timeout=45,
            check=False,
        )
        output = result.stdout + result.stderr
        assert result.returncode == code, (
            f"terraform {args}: exit {result.returncode}\n{output}"
        )
        if error:
            assert error in output, output
        return output

    def state(self):
        data = json.loads((self.root / "terraform.tfstate").read_text())
        return next(
            resource["instances"][0]
            for resource in data["resources"]
            if resource["type"] == "logfire_" + self.kind
        )

    def apply(self):
        self.run("apply", "-auto-approve")
        return self.state()["attributes"]

    def unchanged(self):
        self.run("plan", "-detailed-exitcode")

    def destroy(self):
        self.run("destroy", "-auto-approve")


def project_lifecycle(tf, api):
    tf.config(
        "project",
        'name = "production"\ndescription = "keep this"\nvisibility = "private"',
    )
    assert tf.apply()["id"] == PROJECT_ID
    tf.config(
        "project", 'name = "renamed"\ndescription = "keep this"\nvisibility = "private"'
    )
    state = tf.apply()
    assert (state["name"], state["description"], state["visibility"]) == (
        "renamed",
        "keep this",
        "private",
    ), state
    updates = [payload for method, path, payload in api.requests if method == "PUT"]
    assert updates[-1] == {"project_name": "renamed"}, updates
    tf.unchanged()
    api.objects["/api/v1/projects/"] = {
        "other": {"id": "other", "project_name": PROJECT_ID},
        **api.objects["/api/v1/projects/"],
    }
    tf.run("state", "rm", "logfire_project.test")
    tf.run("import", "logfire_project.test", PROJECT_ID)
    assert tf.state()["attributes"]["id"] == PROJECT_ID
    tf.unchanged()
    tf.config("project", 'name = "renamed"\nvisibility = "private"')
    assert tf.apply()["description"] is None
    tf.unchanged()
    tf.destroy()
    assert PROJECT_ID not in api.objects["/api/v1/projects/"]


def channel_lifecycle(tf, api):
    slack = 'config {\ntype = "slack-integration"\ninstall_id = "install-1"\nchannel_id = "C123"\n%s\n}'
    tf.config("channel", 'name = "slack"\n' + slack % "")
    assert tf.apply()["config"]["include_agent_prompt"] is True
    tf.unchanged()
    tf.config("channel", 'name = "slack"\n' + slack % "include_agent_prompt = false")
    assert tf.apply()["config"]["include_agent_prompt"] is False
    tf.config("channel", 'name = "renamed"\n' + slack % "")
    state = tf.apply()
    assert (
        state["id"],
        state["name"],
        state["active"],
        state["config"]["include_agent_prompt"],
    ) == ("channel-1", "renamed", True, False), state
    tf.unchanged()
    tf.run("state", "rm", "logfire_channel.test")
    tf.run("import", "logfire_channel.test", "channel-1")
    assert tf.state()["attributes"]["config"]["include_agent_prompt"] is False
    tf.unchanged()
    tf.config("channel", 'name = "renamed"\n' + slack % "include_agent_prompt = true")
    assert tf.apply()["config"]["include_agent_prompt"] is True
    tf.config("channel", 'name = "renamed"\nactive = false\n' + WEBHOOK)
    state = tf.apply()
    assert (state["active"], state["config"]["type"], state["config"]["url"]) == (
        False,
        "webhook",
        "https://example.com/hook",
    ), state
    tf.unchanged()
    tf.destroy()
    assert api.objects[CHANNEL_PATH] == {}


def dashboard_lifecycle(tf, api):
    body = f'project_id = "{PROJECT_ID}"\nname = "Original"\nslug = "target"\ndefinition = {json.dumps(json.dumps(DEFINITION, separators=(",", ":")))}'
    tf.config("dashboard", body)
    assert tf.apply()["id"] == "dashboard-1"
    tf.unchanged()
    tf.run("state", "rm", "logfire_dashboard.test")
    tf.run("import", "logfire_dashboard.test", "my-project/target")
    assert tf.state()["attributes"]["id"] == "dashboard-1"
    tf.unchanged()
    tf.config("dashboard", body.replace('"Original"', '"Renamed"'))
    assert tf.apply()["name"] == "Renamed"
    tf.unchanged()
    dashboards = api.objects[PROJECT_PATH + "dashboards/"]
    replacement = {**dashboards.pop("dashboard-1"), "id": "dashboard-2"}
    dashboards["dashboard-2"] = replacement
    tf.run("plan", "-detailed-exitcode", code=2)
    tf.destroy()
    assert dashboards == {"dashboard-2": replacement}, dashboards


def tokens_lifecycle(tf, api):
    for kind in ("read_token", "write_token"):
        tf.config(kind, f'project_id = "{PROJECT_ID}"\nexpires_at = null')
        state = tf.apply()
        assert (
            state["id"],
            state["project_id"],
            state["token"],
            state["expires_at"],
        ) == ("token-1", PROJECT_ID, "mock-created-token", None), state
        tf.unchanged()
        assert tf.state()["attributes"]["token"] == "mock-created-token"
        tf.destroy()
        assert api.objects[PROJECT_PATH + kind.replace("_", "-") + "s/"] == {}


def partial_create(tf, api):
    for kind, body in (
        ("organization", 'name = "acme"\nbilling_email = "billing@example.com"'),
        ("channel", 'name = "channel"\nactive = false\n' + WEBHOOK),
    ):
        tf.config(kind, body)
        api.fail_followup = True
        tf.run("apply", "-auto-approve", code=1, error="saved in state")
        state = tf.state()
        saved_id = {"organization": "org-1", "channel": "channel-1"}[kind]
        assert (state["attributes"]["id"], state["status"]) == (saved_id, "tainted"), (
            state
        )
        if kind == "channel":
            assert state["attributes"]["config"]["url"] == "https://example.com/hook"
        api.fail_followup = False
        tf.run("plan", "-refresh=false", error="must be replaced")
        if kind == "organization":
            tf.run(
                "apply",
                "-auto-approve",
                "-refresh=false",
                code=1,
                error="Organization deletion is protected",
            )
        tf.run("untaint", f"logfire_{kind}.test")
        start = len(api.requests)
        state = tf.apply()
        assert state["id"] == saved_id
        assert (
            state["billing_email"] if kind == "organization" else state["active"]
        ) == ("billing@example.com" if kind == "organization" else False)
        assert not any(
            method == "DELETE" or (method == "POST" and path != "/api/oauth/token")
            for method, path, payload in api.requests[start:]
        )
        tf.unchanged()
        if kind == "organization":
            tf.config(kind, body + "\ndeletion_protection = false")
            tf.apply()
        tf.destroy()
    assert api.objects[ORG_PATH] == api.objects[CHANNEL_PATH] == {}


def invalid_inputs(tf, api):
    for kind, body in (
        ("api_key", 'name = "test"\nscopes = ["organization:read_api_key"]'),
        ("gateway_api_key", f'name = "test"\nproject_id = "{PROJECT_ID}"'),
        ("read_token", f'project_id = "{PROJECT_ID}"'),
        ("write_token", f'project_id = "{PROJECT_ID}"'),
    ):
        for value in ("null", '"2026-12-31T23:59:59Z"'):
            tf.config(kind, body + f"\nexpires_at = {value}")
            tf.run("plan")
        for value in ("", "  ", "not-a-timestamp"):
            tf.config(kind, body + f"\nexpires_at = {json.dumps(value)}")
            tf.run("plan", code=1, error="valid RFC3339 timestamp")
    for definition in ("null", "[]", '"text"', "1", "true"):
        tf.config(
            "dashboard",
            f'project_id = "{PROJECT_ID}"\nname = "bad"\nslug = "bad"\ndefinition = {json.dumps(definition)}',
        )
        tf.run("apply", "-auto-approve", code=1, error="invalid dashboard definition")
    tf.config(
        "slo",
        f'project_id = "{PROJECT_ID}"\nscope_value = "payments"\nname = "bad"\nsource = "metrics"\nmetric_aggregation = "histogram_threshold"\ntotal_query = "true"\nbad_query = "true"\nthreshold = "60"\ncomparison = "less_than"\ntarget_percent = "99.9"\nrolling_window = "30d"',
    )
    tf.run("plan", code=1, error="bad_query is not valid for histogram_threshold")
    assert api.created == [], api.created


def failed_creates(tf, api):
    project = f'project_id = "{PROJECT_ID}"\n'
    configs = {
        "organization": 'name = "acme"',
        "project": 'name = "test"',
        "channel": 'name = "test"\n' + WEBHOOK,
        "read_token": project,
        "write_token": project,
        "dashboard": project
        + f'name = "test"\nslug = "test"\ndefinition = {json.dumps(json.dumps(DEFINITION, separators=(",", ":")))}',
        "alert": project
        + 'name = "test"\nquery = "SELECT 1"\ntime_window = "5m"\nfrequency = "1m"\nnotify_when = "has_matches"\nchannel_assignments = []',
        "slo": project
        + 'scope_value = "payments"\nname = "test"\ntotal_query = "true"\nbad_query = "false"\ntarget_percent = "99.9"\nrolling_window = "30d"',
    }
    for kind, body in configs.items():
        for failure in (503, 429, "disconnect"):
            tf.config(kind, body)
            api.create_failure = failure
            start = len(api.created)
            tf.run("apply", "-auto-approve", code=1, error="failed")
            assert len(api.created[start:]) == 1, (
                f"{kind} after {failure} created {api.created[start:]}"
            )


def main():
    repo = Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix="logfire-e2e-") as scratch:
        root = Path(scratch)
        subprocess.run(
            ["go", "build", "-o", str(root / "terraform-provider-logfire"), "."],
            cwd=repo,
            check=True,
        )
        cli = root / "terraform.tfrc"
        cli.write_text(
            f'provider_installation {{\ndev_overrides {{\n"pydantic/logfire" = "{root}"\n}}\ndirect {{}}\n}}'
        )
        for check in (
            project_lifecycle,
            channel_lifecycle,
            dashboard_lifecycle,
            tokens_lifecycle,
            partial_create,
            invalid_inputs,
            failed_creates,
        ):
            with API() as api:
                thread = threading.Thread(target=api.serve_forever, daemon=True)
                thread.start()
                try:
                    check(Terraform(root / check.__name__, api, cli), api)
                    print(f"PASS {check.__name__}", flush=True)
                finally:
                    api.shutdown()
                    thread.join()


if __name__ == "__main__":
    main()
