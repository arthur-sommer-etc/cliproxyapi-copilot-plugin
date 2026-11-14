import hashlib
import json
import tempfile
import unittest
from pathlib import Path, PurePosixPath


ROOT = Path(__file__).resolve().parent.parent
MANIFEST = ROOT / "tools" / "captured-json-fixtures.json"
# Update this inventory fingerprint deliberately when adding or retiring captures.
INVENTORY_SHA256 = "07b9e45da4f6c0585ebb31e67a22d4859e199f5f81cf415a53f7f2b3777df399"


def reject_non_json_number(value):
    raise ValueError(f"invalid JSON number {value}")


def validate_captures(root, entries):
    if not isinstance(entries, list) or not entries:
        raise ValueError("capture manifest must be a nonempty array")
    seen = set()
    for entry in entries:
        if not isinstance(entry, dict) or set(entry) != {"path", "sha256"}:
            raise ValueError("invalid capture manifest entry")
        name, digest = entry["path"], entry["sha256"]
        if not isinstance(name, str) or not isinstance(digest, str):
            raise ValueError("invalid capture manifest types")
        path = PurePosixPath(name)
        if (path.is_absolute() or path.as_posix() != name or ".." in path.parts or "testdata" not in path.parts
                or path.suffix != ".json" or any(c in name for c in "*?[]!\\")):
            raise ValueError(f"invalid capture path: {name}")
        if name in seen:
            raise ValueError(f"duplicate capture path: {name}")
        seen.add(name)
        if len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
            raise ValueError(f"invalid capture hash: {name}")
        fixture = root.joinpath(*path.parts)
        for parent in (fixture, *fixture.parents):
            if parent == root:
                break
            if parent.is_symlink():
                raise ValueError(f"capture path contains symlink: {name}")
        if not fixture.is_file() or not fixture.resolve().is_relative_to(root.resolve()):
            raise ValueError(f"capture missing or outside repository: {name}")
        body = fixture.read_bytes()
        if hashlib.sha256(body).hexdigest() != digest:
            raise ValueError(f"capture hash mismatch: {name}")
        try:
            json.loads(body, parse_constant=reject_non_json_number)
        except (ValueError, UnicodeDecodeError) as error:
            raise ValueError(f"invalid capture JSON: {name}") from error
    return seen


def expected_capture_paths(root):
    translate = root / "internal" / "translate" / "testdata"
    paths = set(translate.glob("live-*/*.json"))
    for directory in ("claude-gpt-pdf-read", "claude-gpt-png-read"):
        paths.update(path for path in (translate / directory / "bodies").glob("*.json") if not path.name.endswith(".shape.json"))
    for directory in ("native-attachment-captures", "native-hosted-tool-stream-captures", "native-pdf-tool-captures", "native-server-tool-captures"):
        paths.update((translate / directory).glob("*/request.json"))
        paths.update((translate / directory).glob("*/response.json"))
    provider = root / "internal" / "provider" / "testdata"
    paths.update((provider / "codex-cached-search" / "request.json", provider / "codex-cached-search" / "response.json", provider / "current-copilot-catalog" / "response.json"))
    return {path.relative_to(root).as_posix() for path in paths}


class CapturedJSONFixturesTest(unittest.TestCase):
    def test_capture_bytes_and_inventory(self):
        entries = json.loads(MANIFEST.read_bytes(), parse_constant=reject_non_json_number)
        actual = validate_captures(ROOT, entries)
        expected = expected_capture_paths(ROOT)
        self.assertEqual(actual, expected, f"capture manifest mismatch: missing={sorted(expected - actual)}, extra={sorted(actual - expected)}")
        inventory = ("\n".join(sorted(actual)) + "\n").encode()
        self.assertEqual(hashlib.sha256(inventory).hexdigest(), INVENTORY_SHA256, "capture inventory changed; review and update its fingerprint")

    def test_rejects_mutation_invalid_json_missing_duplicate_and_traversal(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "testdata" / "capture.json"
            fixture.parent.mkdir()
            fixture.write_bytes(b'{"value":1}')
            entry = {"path": "testdata/capture.json", "sha256": hashlib.sha256(fixture.read_bytes()).hexdigest()}
            self.assertEqual(validate_captures(root, [entry]), {entry["path"]})
            with self.assertRaisesRegex(ValueError, "duplicate capture"):
                validate_captures(root, [entry, entry])
            with self.assertRaisesRegex(ValueError, "invalid capture path"):
                validate_captures(root, [{**entry, "path": "../testdata/capture.json"}])
            with self.assertRaisesRegex(ValueError, "invalid capture path"):
                validate_captures(root, [{**entry, "path": "testdata/*.json"}])
            link = fixture.parent / "link.json"
            link.symlink_to(fixture)
            with self.assertRaisesRegex(ValueError, "symlink"):
                validate_captures(root, [{**entry, "path": "testdata/link.json"}])
            fixture.write_bytes(b'{"value":2}')
            with self.assertRaisesRegex(ValueError, "hash mismatch"):
                validate_captures(root, [entry])
            fixture.write_bytes(b'{"value":NaN}')
            invalid = {**entry, "sha256": hashlib.sha256(fixture.read_bytes()).hexdigest()}
            with self.assertRaisesRegex(ValueError, "invalid capture JSON"):
                validate_captures(root, [invalid])
            fixture.unlink()
            with self.assertRaisesRegex(ValueError, "capture missing"):
                validate_captures(root, [entry])


if __name__ == "__main__":
    unittest.main()
