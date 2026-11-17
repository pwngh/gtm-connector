"""Give an external Inspector client an explicit local grant."""

import json
from pathlib import Path

import httpx2

from .client import LocalClient
from .runtime import private_file


def write_inspector_config(directory, scopes):
    """Keep the bearer credential in a private file, not terminal output."""
    directory = Path(directory).resolve()
    with (directory / "owner.json").open() as source:
        owner = json.load(source)
    if not isinstance(owner, dict) or not {
        "issuer", "resource", "redirect_uri", "client_id", "owner_secret", "cert_path"
    } <= owner.keys():
        raise ValueError("Invalid owner configuration; start serve in this state directory.")
    try:
        with LocalClient(owner) as client:
            token = client.authorize(scopes)
    except httpx2.HTTPError:
        raise RuntimeError("Cannot reach the local authority; start serve in this state directory.") from None
    config = {"mcpServers": {"gtm-connector": {
        "type": "streamable-http", "url": owner["resource"],
        "protocolEra": "modern", "headers": {"Authorization": "Bearer " + token},
    }}}
    path = directory / "inspector.json"
    private_file(path, json.dumps(config, indent=2).encode())
    return {"config": str(path), "certificate": str(owner["cert_path"]),
            "scopes": sorted(set(scopes))}
