import hashlib
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


PACKAGING = Path(__file__).parent
VERSION = "1.2.3.4"


class GoReleaserPrepareTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="release fixture ")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        packaging = self.root / "packaging"
        packaging.mkdir()
        for name in ("goreleaser-prepare.sh", "package-release.sh"):
            target = packaging / name
            target.write_bytes((PACKAGING / name).read_bytes())
            target.chmod(0o755)

        bin_dir = self.root / "bin"
        bin_dir.mkdir()
        task = bin_dir / "task"
        task.write_text(
            """#!/bin/sh
set -eu
[ "$#" -eq 2 ]
printf '%s|%s\\n' "$1" "$2" >> "$TASK_CAPTURE"
case "$1" in
  package) target=linux_amd64 ;;
  package-darwin) target=darwin_arm64 ;;
  *) exit 1 ;;
esac
case "$2" in VERSION=*) version=${2#VERSION=} ;; *) exit 1 ;; esac
mkdir -p dist
archive="cliproxyapi-copilot_${version}_${target}.zip"
printf 'synthetic archive %s\\n' "$target" > "dist/$archive"
(cd dist && shasum -a 256 "$archive") > dist/checksums.txt
if [ "${CORRUPT_CHECKSUM:-0}" = 1 ] && [ "$target" = darwin_arm64 ]; then
  printf 'invalid checksum  %s\\n' "$archive" > dist/checksums.txt
fi
"""
        )
        task.chmod(0o755)
        self.capture = self.root / "task calls"
        self.env = {
            **os.environ,
            "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
            "TASK_CAPTURE": str(self.capture),
            "RELEASE_VERSION": VERSION,
            "RELEASE_BUILD_PACKAGES": "1",
            "CORRUPT_CHECKSUM": "0",
        }

    def run_hook(self, **overrides):
        return subprocess.run(
            ["sh", "packaging/goreleaser-prepare.sh"],
            cwd=self.root,
            env={**self.env, **overrides},
            check=False,
            capture_output=True,
            text=True,
        )

    def test_builds_both_packages_with_version_and_combines_checked_sums(self):
        result = self.run_hook()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            self.capture.read_text(),
            f"package|VERSION={VERSION}\npackage-darwin|VERSION={VERSION}\n",
        )
        expected = []
        for target in ("linux_amd64", "darwin_arm64"):
            name = f"cliproxyapi-copilot_{VERSION}_{target}.zip"
            archive = self.root / "dist" / name
            expected.append(f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {name}\n")
        self.assertEqual((self.root / "dist" / "checksums.txt").read_text(), "".join(expected))

    def test_rejects_incorrect_package_checksum(self):
        result = self.run_hook(CORRUPT_CHECKSUM="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(
            self.capture.read_text(),
            f"package|VERSION={VERSION}\npackage-darwin|VERSION={VERSION}\n",
        )

    def test_rejects_invalid_version_before_build(self):
        result = self.run_hook(RELEASE_VERSION='1.2.3";id;#')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("version must be dotted numeric", result.stderr)
        self.assertFalse(self.capture.exists())


if __name__ == "__main__":
    unittest.main()
