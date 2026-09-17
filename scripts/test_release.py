import hashlib
import json
from pathlib import Path
import shutil
import tarfile
import tempfile
import unittest
import zipfile

import release


class ReleaseTest(unittest.TestCase):
    def test_complete_release_and_checksum_pinned_manifests(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "binary"
            binary.write_bytes(b"standalone executable")
            output = root / "dist"
            for target in release.TARGETS:
                archive = release.package("1.2.3", target, binary, output)
                executable = output / release.binary_name("1.2.3", target)
                self.assertEqual(executable.read_bytes(), binary.read_bytes())
                self.assertEqual(executable.stat().st_mode & 0o777, 0o755)
                if target.startswith("windows"):
                    with zipfile.ZipFile(archive) as bundle:
                        self.assertEqual(bundle.read("steward.exe"), binary.read_bytes())
                        self.assertIn("LICENSE", bundle.namelist())
                else:
                    with tarfile.open(archive) as bundle:
                        self.assertEqual(bundle.extractfile("steward").read(), binary.read_bytes())
                        self.assertEqual(bundle.getmember("steward").mode, 0o755)
                        self.assertIn("LICENSE", bundle.getnames())
            with self.assertRaises(FileNotFoundError):
                release.finalize("1.2.3", output)
            for arch in ("amd64", "arm64"):
                for kind in ("deb", "rpm"):
                    (output / f"steward_1.2.3_linux_{arch}.{kind}").write_bytes(b"package payload")
            shutil.copyfile(release.ROOT / "install.sh", output / "install.sh")
            release.finalize("1.2.3", output)
            prepared = root / "prepared"
            release.prepare_binaries("1.2.3", output, prepared)
            self.assertEqual(sorted(p.name for p in prepared.iterdir()), sorted(release.binary_name("1.2.3", target) for target in release.TARGETS))
            for executable in prepared.iterdir():
                self.assertEqual(executable.read_bytes(), binary.read_bytes())
            for line in (output / "checksums.txt").read_text().splitlines():
                checksum, name = line.split()
                self.assertEqual(checksum, hashlib.sha256((output / name).read_bytes()).hexdigest())
            formula = (output / "steward.rb").read_text()
            for target in release.TARGETS[:-1]:
                name = release.archive_name("1.2.3", target)
                self.assertIn(f"releases/download/v1.2.3/{name}", formula)
                self.assertIn(hashlib.sha256((output / name).read_bytes()).hexdigest(), formula)
            scoop = json.loads((output / "steward.json").read_text())
            name = release.archive_name("1.2.3", "windows_amd64")
            self.assertTrue(scoop["architecture"]["64bit"]["url"].endswith(name))
            self.assertEqual(scoop["architecture"]["64bit"]["hash"], hashlib.sha256((output / name).read_bytes()).hexdigest())
            (output / release.binary_name("1.2.3", "linux_arm64")).unlink()
            with self.assertRaises(FileNotFoundError):
                release.finalize("1.2.3", output)
            (output / release.archive_name("1.2.3", "darwin_amd64")).write_bytes(b"corrupt archive")
            with self.assertRaisesRegex(ValueError, "Checksum mismatch"):
                release.prepare_binaries("1.2.3", output, prepared)


if __name__ == "__main__":
    unittest.main()
