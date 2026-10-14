#!/usr/bin/env python3
# Copyright Pydantic, Inc. 2025, 2026
# SPDX-License-Identifier: MPL-2.0

"""Refresh the API-key contracts used by the offline client tests."""

import argparse
import hashlib
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]
CONTRACTS = ROOT / "internal/client/testdata/api-contracts"
PATHS = ("/v1/api-keys/", "/v1/api-keys/{api_key_id}/")


def references(value):
    if isinstance(value, dict):
        if "$ref" in value:
            yield value["$ref"]
        for child in value.values():
            yield from references(child)
    elif isinstance(value, list):
        for child in value:
            yield from references(child)


def extract(document):
    selected = {
        key: document[key]
        for key in ("openapi", "info", "servers", "security", "jsonSchemaDialect")
        if key in document
    }
    selected["paths"] = {path: document["paths"][path] for path in PATHS}
    selected["components"] = {}
    security = list(document.get("security", []))
    for item in selected["paths"].values():
        for operation in item.values():
            if isinstance(operation, dict):
                security.extend(operation.get("security", []))
    schemes = {name for requirement in security for name in requirement}
    if schemes:
        selected["components"]["securitySchemes"] = {
            name: document["components"]["securitySchemes"][name]
            for name in sorted(schemes)
        }
    pending = list(references(selected))
    while pending:
        reference = pending.pop()
        parts = reference.split("/")
        if len(parts) < 4 or parts[:2] != ["#", "components"]:
            raise ValueError(f"Unsupported contract reference: {reference}")
        section, name = (part.replace("~1", "/").replace("~0", "~") for part in parts[2:4])
        output = selected["components"].setdefault(section, {})
        if name not in output:
            component = document["components"][section][name]
            output[name] = component
            pending.extend(references(component))
    return selected


def main():
    manifest_path = CONTRACTS / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--contract", choices=[item["name"] for item in manifest["contracts"]])
    parser.add_argument("--platform-repo", type=Path)
    args = parser.parse_args()
    for contract in manifest["contracts"]:
        if args.contract and contract["name"] != args.contract:
            continue
        source = contract["source"]
        if source["kind"] == "git":
            if args.platform_repo is None:
                parser.error("--platform-repo is required for the released platform contract")
            raw = subprocess.check_output(
                ["git", "-C", str(args.platform_repo), "show", f"{source['revision']}:{source['schema_path']}"]
            )
        elif source["kind"] == "http":
            with urlopen(source["url"], timeout=30) as response:
                raw = response.read()
            if source.get("sha256") != hashlib.sha256(raw).hexdigest():
                source["captured_at"] = datetime.now(timezone.utc).isoformat()
        else:
            raise ValueError(f"Unknown contract source: {source['kind']}")
        source["sha256"] = hashlib.sha256(raw).hexdigest()
        snapshot = (json.dumps(extract(json.loads(raw)), indent=2, sort_keys=True) + "\n").encode()
        contract["sha256"] = hashlib.sha256(snapshot).hexdigest()
        (CONTRACTS / contract["file"]).write_bytes(snapshot)
        print(f"{contract['name']}: {len(snapshot)} bytes, sha256 {contract['sha256']}")
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")


if __name__ == "__main__":
    main()
