"""Resolve the selected CLIProxyAPI host module for release metadata.

The package metadata must identify the module selected by Go, including any
version-scoped replacement that takes precedence over a wildcard replacement.
"""

from __future__ import annotations

import json
import os
import pathlib
import subprocess
from collections.abc import Mapping
from typing import Any

SDK_MODULE = "github.com/router-for-me/CLIProxyAPI/v8"
MAINTAINED_HOST_MODULE = "github.com/ririnto/CLIProxyAPI/v8"
REQUIRED_HOST_CALLBACKS = ("host.payload.finalize",)


class HostRequirementsError(ValueError):
    """Report invalid or unsupported host module selections."""


def build_host_requirements(
    module: Mapping[str, Any], plugin_id: str, plugin_version: str
) -> dict[str, Any]:
    """Build release metadata from Go's selected host module identity."""
    if module.get("Path") != SDK_MODULE:
        raise HostRequirementsError(f"Go selected an unexpected SDK module: {module.get('Path')!r}")

    sdk_version = module.get("Version")
    if not isinstance(sdk_version, str) or not sdk_version:
        raise HostRequirementsError("Go did not report a version for the selected SDK module")

    replacement = module.get("Replace")
    if not isinstance(replacement, Mapping):
        raise HostRequirementsError(
            f"release package requires a published version of {MAINTAINED_HOST_MODULE} "
            "to provide host.payload.finalize"
        )

    host_module = replacement.get("Path")
    host_version = replacement.get("Version")
    if (
        host_module != MAINTAINED_HOST_MODULE
        or not isinstance(host_version, str)
        or not host_version
    ):
        raise HostRequirementsError(
            "release package requires Go's selected host replacement to be a published "
            f"version of {MAINTAINED_HOST_MODULE}"
        )

    return {
        "schema_version": 1,
        "plugin": {"id": plugin_id, "version": plugin_version},
        "plugin_abi": {"module": SDK_MODULE, "version": sdk_version},
        "required_host": {
            "module": host_module,
            "version": host_version,
            "callbacks": list(REQUIRED_HOST_CALLBACKS),
        },
    }


def selected_host_requirements(
    plugin_id: str, plugin_version: str, repository: pathlib.Path
) -> dict[str, Any]:
    """Resolve Go's selected CLIProxyAPI module without changing the graph."""
    environment = os.environ.copy()
    environment.update({"GOWORK": "off", "GOFLAGS": "", "GOTOOLCHAIN": "local"})
    result = subprocess.run(
        ["go", "list", "-mod=readonly", "-m", "-json", SDK_MODULE],
        cwd=repository,
        env=environment,
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        detail = result.stderr.strip()
        raise HostRequirementsError(
            "could not resolve the selected CLIProxyAPI host module"
            + (f": {detail}" if detail else "")
        )

    try:
        module = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise HostRequirementsError("go list returned invalid module JSON") from error
    if not isinstance(module, Mapping):
        raise HostRequirementsError("go list returned an invalid module record")
    return build_host_requirements(module, plugin_id, plugin_version)
