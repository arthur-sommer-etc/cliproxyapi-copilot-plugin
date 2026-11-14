import hashlib
import json
import os
import subprocess
import tempfile
import unittest
import zipfile
from pathlib import Path


SCRIPT = Path(__file__).with_name("package-release.sh")
PLUGIN_ID = "cliproxyapi-copilot"
VERSION = "0.3.3"


class PackageReleaseTest(unittest.TestCase):
    def test_supported_targets_create_named_archives_and_checksums(self):
        targets = (
            ("linux", "amd64", f"{PLUGIN_ID}.so"),
            ("darwin", "arm64", f"{PLUGIN_ID}.dylib"),
        )
        for goos, goarch, filename in targets:
            with self.subTest(goos=goos, goarch=goarch):
                with tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    script = root / "packaging" / SCRIPT.name
                    script.parent.mkdir()
                    script.write_bytes(SCRIPT.read_bytes())
                    (script.parent / "host_requirements.py").write_bytes(
                        SCRIPT.with_name("host_requirements.py").read_bytes()
                    )
                    bin_dir = root / "bin"
                    bin_dir.mkdir()
                    go = bin_dir / "go"
                    go.write_text(
                        "#!/bin/sh\nprintf '%s\\n' "
                        "'{\"Path\":\"github.com/router-for-me/CLIProxyAPI/v8\","
                        "\"Version\":\"v8.0.30\",\"Replace\":{"
                        "\"Path\":\"github.com/ririnto/CLIProxyAPI/v8\","
                        "\"Version\":\"v8.0.1\"}}'\n"
                    )
                    go.chmod(0o755)
                    plugin = root / "build" / "plugins" / goos / goarch / filename
                    plugin.parent.mkdir(parents=True)
                    plugin.write_bytes(b"release fixture")
                    result = subprocess.run(
                        ["sh", str(script), VERSION, goos, goarch],
                        check=False,
                        capture_output=True,
                        text=True,
                        env={**os.environ, "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}"},
                    )
                    archive_name = f"{PLUGIN_ID}_{VERSION}_{goos}_{goarch}.zip"
                    archive = root / "dist" / archive_name
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertTrue(archive.is_file())
                    with zipfile.ZipFile(archive) as package:
                        metadata_name = f"{PLUGIN_ID}.host-requirements.json"
                        self.assertEqual(package.namelist(), [filename, metadata_name])
                        self.assertEqual(package.read(filename), b"release fixture")
                        metadata = json.loads(package.read(metadata_name))
                        self.assertEqual(metadata["plugin"], {"id": PLUGIN_ID, "version": VERSION})
                        self.assertEqual(metadata["required_host"], {
                            "module": "github.com/ririnto/CLIProxyAPI/v8",
                            "version": "v8.0.1",
                            "callbacks": ["host.payload.finalize"],
                        })
                    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
                    self.assertEqual(
                        (root / "dist" / "checksums.txt").read_text(),
                        f"{digest}  {archive_name}\n",
                    )

    def test_unsupported_target_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / "packaging" / SCRIPT.name
            script.parent.mkdir()
            script.write_bytes(SCRIPT.read_bytes())
            result = subprocess.run(
                ["sh", str(script), VERSION, "windows", "amd64"],
                check=False,
                capture_output=True,
                text=True,
            )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unsupported target", result.stderr)


class ReleaseVersionTest(unittest.TestCase):
    def test_supported_dotted_numeric_versions_are_returned(self):
        for version in ("1.2.3", "1.2.3.4"):
            with self.subTest(version=version):
                result = subprocess.run(
                    ["sh", str(SCRIPT), "--validate-version", version],
                    check=False,
                    capture_output=True,
                    text=True,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, f"{version}\n")

    def test_shell_metacharacters_are_rejected_before_make(self):
        result = subprocess.run(
            ["sh", str(SCRIPT), "--validate-version", '1.2.3";id;#'],
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, "")
        self.assertIn("version must be dotted numeric", result.stderr)


class ReleaseGitValidationTest(unittest.TestCase):
    def test_dotted_tag_requires_clean_worktree_and_exact_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            packaging = root / "packaging"
            packaging.mkdir()
            for name in ("package-release.sh", "validate-release-git.sh"):
                (packaging / name).write_bytes(SCRIPT.with_name(name).read_bytes())
                (packaging / name).chmod(0o755)
            env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
            env.update({
                "GIT_CONFIG_NOSYSTEM": "1",
                "GIT_CONFIG_GLOBAL": os.devnull,
                "GIT_AUTHOR_NAME": "Release Test",
                "GIT_AUTHOR_EMAIL": "release@example.invalid",
                "GIT_COMMITTER_NAME": "Release Test",
                "GIT_COMMITTER_EMAIL": "release@example.invalid",
            })
            def git(*args):
                return subprocess.run(
                    ["git", *args], cwd=root, env=env, check=True, capture_output=True, text=True
                )
            git("init", "-q")
            git("add", "packaging")
            git("commit", "-qm", "release")
            git("tag", "v1.2.3.4")
            git("tag", "v1.2.3")
            def validate(tag):
                return subprocess.run(
                    ["sh", "packaging/validate-release-git.sh"],
                    cwd=root, env={**env, "RELEASE_TAG": tag},
                    check=False, capture_output=True, text=True,
                )
            self.assertEqual(validate("v1.2.3.4").returncode, 0)
            self.assertEqual(validate("v1.2.3").returncode, 0)
            self.assertNotEqual(validate("v1.2.4").returncode, 0)
            self.assertNotEqual(validate("v1.2.3.4-invalid").returncode, 0)
            original = (packaging / "package-release.sh").read_bytes()
            (packaging / "package-release.sh").write_bytes(original + b"\n")
            self.assertNotEqual(validate("v1.2.3.4").returncode, 0)
            (packaging / "package-release.sh").write_bytes(original)
            (root / "untracked").write_text("dirty")
            self.assertNotEqual(validate("v1.2.3.4").returncode, 0)
            git("add", "untracked")
            git("commit", "-qm", "next")
            self.assertNotEqual(validate("v1.2.3.4").returncode, 0)


if __name__ == "__main__":
    unittest.main()
