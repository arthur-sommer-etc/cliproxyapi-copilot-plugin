"""Test release metadata against selected Go module identities."""

from __future__ import annotations

import json
import os
import pathlib
import tempfile
import unittest
from unittest.mock import patch

from host_requirements import (
    HostRequirementsError,
    MAINTAINED_HOST_MODULE,
    SDK_MODULE,
    build_host_requirements,
    selected_host_requirements,
)


class HostRequirementsTests(unittest.TestCase):
    """Verify that release metadata follows Go's selected module source."""

    def test_builds_metadata_for_the_selected_maintained_host(self) -> None:
        module = {
            "Path": SDK_MODULE,
            "Version": "v8.0.23",
            "Replace": {
                "Path": MAINTAINED_HOST_MODULE,
                "Version": "v8.0.0-20261010033233-a22e8f7c0065",
                "Dir": "/module/cache/path",
            },
        }

        requirements = build_host_requirements(module, "cliproxyapi-copilot", "0.3.3")

        self.assertEqual(requirements["plugin_abi"]["version"], "v8.0.23")
        self.assertEqual(requirements["required_host"]["module"], MAINTAINED_HOST_MODULE)
        self.assertEqual(
            requirements["required_host"]["callbacks"], ["host.payload.finalize"]
        )

    def test_rejects_selected_official_host(self) -> None:
        module = {
            "Path": SDK_MODULE,
            "Version": "v8.0.23",
            "Replace": {"Path": SDK_MODULE, "Version": "v8.0.23"},
        }

        with self.assertRaisesRegex(HostRequirementsError, "selected host replacement"):
            build_host_requirements(module, "cliproxyapi-copilot", "0.3.3")

    def test_rejects_local_host_replacement(self) -> None:
        module = {
            "Path": SDK_MODULE,
            "Version": "v8.0.23",
            "Replace": {"Path": "../CLIProxyAPI", "Dir": "/tmp/CLIProxyAPI"},
        }

        with self.assertRaisesRegex(HostRequirementsError, "published version"):
            build_host_requirements(module, "cliproxyapi-copilot", "0.3.3")

    def test_version_scoped_official_replacement_wins_over_fork_wildcard(self) -> None:
        with tempfile.TemporaryDirectory(prefix="host-requirements-test-") as temporary:
            root = pathlib.Path(temporary)
            repository = root / "repository"
            proxy_version = (
                root
                / "proxy"
                / "github.com"
                / "router-for-me"
                / "!c!l!i!proxy!a!p!i"
                / "v8"
                / "@v"
            )
            repository.mkdir()
            proxy_version.mkdir(parents=True)
            (proxy_version / "v8.0.23.mod").write_text(
                f"module {SDK_MODULE}\n\ngo 1.26.0\n", encoding="utf-8"
            )
            (proxy_version / "v8.0.23.info").write_text(
                json.dumps({"Version": "v8.0.23", "Time": "2026-10-09T13:35:40Z"}),
                encoding="utf-8",
            )
            (repository / "go.mod").write_text(
                f"module example.test/root\n\ngo 1.27\n\n"
                f"require {SDK_MODULE} v8.0.23\n\n"
                f"replace {SDK_MODULE} => {MAINTAINED_HOST_MODULE} "
                "v8.0.0-20261010033233-a22e8f7c0065\n"
                f"replace {SDK_MODULE} v8.0.23 => {SDK_MODULE} v8.0.23\n",
                encoding="utf-8",
            )
            environment = os.environ.copy()
            environment.update(
                {
                    "GOENV": "off",
                    "GOMODCACHE": str(root / "module-cache"),
                    "GOPROXY": (root / "proxy").as_uri(),
                    "GOSUMDB": "off",
                }
            )

            with patch.dict(os.environ, environment):
                with self.assertRaisesRegex(
                    HostRequirementsError, "selected host replacement"
                ):
                    selected_host_requirements(
                        "cliproxyapi-copilot", "0.3.3", repository
                    )


if __name__ == "__main__":
    unittest.main()
