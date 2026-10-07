# Copyright Pydantic, Inc. 2025, 2026
# SPDX-License-Identifier: MPL-2.0

"""Verify failed-create state and recovery against a loopback mock API."""

import atexit
import http.server
import json
import os
import pathlib
import subprocess
import tempfile
import threading

repo_dir = pathlib.Path(__file__).resolve().parents[1]
run_dir = tempfile.TemporaryDirectory(prefix='logfire-partial-create-proof-')
atexit.register(run_dir.cleanup)
binary_dir = pathlib.Path(run_dir.name)
subprocess.run(['go', 'build', '-o', str(binary_dir / 'terraform-provider-logfire'), '.'], cwd=repo_dir, check=True)
calls = []
phase = 'fail'
org = {'id': '9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff', 'organization_name': 'acme'}
channel = {'id': 'channel-1', 'label': 'channel', 'active': True, 'config': {'type':'webhook','format':'auto','url':'https://example.com/hook'}}

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass
    def read_json(self):
        if self.headers.get('Content-Length'):
            raw = self.rfile.read(int(self.headers['Content-Length']))
        else:
            chunks = []
            while True:
                size = int(self.rfile.readline().split(b';')[0], 16)
                if not size:
                    self.rfile.readline()
                    break
                chunks.append(self.rfile.read(size))
                self.rfile.read(2)
            raw = b''.join(chunks)
        return json.loads(raw)
    def do_POST(self):
        calls.append((self.command, self.path))
        if self.path == '/api/v1/instance/organizations/':
            self.answer(201, org)
        elif self.path == '/api/oauth/token':
            self.answer(400, {'error':'invalid_scope','error_description':'temporary mock rejection'}) if phase == 'fail' else self.answer(200, {'access_token':'mock-org-token','expires_in':900})
        elif self.path == '/api/v1/channels/':
            self.answer(201, channel)
        else:
            self.answer(404, {})
    def do_GET(self):
        calls.append((self.command, self.path))
        if self.path == '/api/v1/organization/':
            self.answer(200, org)
        elif self.path == '/api/v1/channels/channel-1/':
            self.answer(200, channel)
        else:
            self.answer(404, {})
    def do_PUT(self):
        calls.append((self.command, self.path))
        if phase == 'fail':
            self.answer(400, {'detail':'temporary mock rejection'})
        elif self.path == '/api/v1/channels/channel-1/':
            payload = self.read_json()
            assert payload['active'] is False and payload.get('label', 'channel') == 'channel', payload
            channel['active'] = payload['active']
            self.answer(200, channel)
        elif self.path == '/api/v1/organization/':
            payload = self.read_json()
            org['billing_email'] = payload['billing_email']
            self.answer(200, org)
        else:
            self.answer(404, {})
    def do_DELETE(self):
        calls.append((self.command, self.path))
        self.answer(204, None)
    def answer(self, status, payload):
        raw = json.dumps(payload).encode() if payload is not None else b''
        self.send_response(status)
        self.send_header('Content-Type','application/json')
        self.send_header('Content-Length',str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()
with tempfile.TemporaryDirectory(prefix='logfire-partial-create-') as scratch:
    root = pathlib.Path(scratch)
    cli = root / 'terraform.tfrc'
    cli.write_text(f'provider_installation {{\n  dev_overrides {{\n    "pydantic/logfire" = "{binary_dir}"\n  }}\n  direct {{}}\n}}\n')
    env = os.environ.copy()
    env.update({'TF_CLI_CONFIG_FILE':str(cli), 'TF_IN_AUTOMATION':'1', 'TF_INPUT':'0'})
    configs = {
      'organization': 'resource "logfire_organization" "test" {\nname = "acme"\nbilling_email = "billing@example.com"\n}',
      'channel': 'resource "logfire_channel" "test" {\nname = "channel"\nactive = false\nconfig {\ntype = "webhook"\nformat = "auto"\nurl = "https://example.com/hook"\n}\n}',
    }
    summary = {}
    for kind, config in configs.items():
        phase = 'fail'
        work = root / kind
        work.mkdir()
        (work / 'main.tf').write_text(f'terraform {{\nrequired_providers {{\nlogfire = {{ source = "pydantic/logfire" }}\n}}\n}}\nprovider "logfire" {{\nbase_url = "http://127.0.0.1:{server.server_port}"\napi_key = "mock-provider-token"\n}}\n{config}\n')
        first = subprocess.run(['terraform','apply','-auto-approve','-no-color'],cwd=work,env=env,text=True,capture_output=True,timeout=40)
        (binary_dir / f'{kind}-first-apply.txt').write_text(first.stdout + first.stderr)
        assert first.returncode != 0, first.stdout + first.stderr
        state = json.loads((work / 'terraform.tfstate').read_text())
        instance = state['resources'][0]['instances'][0]
        assert instance['attributes']['id'], state
        assert instance.get('status') == 'tainted', state
        phase = 'healthy'
        second = subprocess.run(['terraform','plan','-no-color','-refresh=false'],cwd=work,env=env,text=True,capture_output=True,timeout=40)
        (binary_dir / f'{kind}-second-plan.txt').write_text(second.stdout + second.stderr)
        assert second.returncode == 0, second.stdout + second.stderr
        assert 'tainted' in second.stdout and 'must be replaced' in second.stdout, second.stdout
        item = {'saved_id':instance['attributes']['id'], 'saved_status':instance['status'], 'next_plan':'replace'}
        if kind == 'organization':
            retry = subprocess.run(['terraform','apply','-auto-approve','-no-color','-refresh=false'],cwd=work,env=env,text=True,capture_output=True,timeout=40)
            (binary_dir / 'organization-retry-apply.txt').write_text(retry.stdout + retry.stderr)
            assert retry.returncode != 0 and 'Organization deletion is protected' in retry.stderr, retry.stdout + retry.stderr
            item['retry_apply'] = 'blocked by deletion protection'
        untaint = subprocess.run(['terraform','untaint','-no-color',f'logfire_{kind}.test'],cwd=work,env=env,text=True,capture_output=True,timeout=40)
        assert untaint.returncode == 0, untaint.stdout + untaint.stderr
        recovery_start = len(calls)
        recovered = subprocess.run(['terraform','apply','-auto-approve','-no-color'],cwd=work,env=env,text=True,capture_output=True,timeout=40)
        (binary_dir / f'{kind}-recovered-apply.txt').write_text(recovered.stdout + recovered.stderr)
        assert recovered.returncode == 0, recovered.stdout + recovered.stderr
        recovery_calls = calls[recovery_start:]
        assert not any(method == 'DELETE' or (method == 'POST' and path != '/api/oauth/token') for method,path in recovery_calls), recovery_calls
        state = json.loads((work / 'terraform.tfstate').read_text())
        instance = state['resources'][0]['instances'][0]
        assert instance['attributes']['id'] == item['saved_id'] and instance.get('status') != 'tainted', instance
        item['after_untaint'] = 'updates existing resource without create or delete'
        summary[kind] = item
    (binary_dir / 'partial-create-result.json').write_text(json.dumps(summary, indent=2) + '\n')
    print(json.dumps(summary, indent=2))
server.shutdown()
