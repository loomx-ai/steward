import hashlib
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent


@unittest.skipIf(os.name == "nt", "POSIX installer")
class InstallTest(unittest.TestCase):
    def test_platforms_latest_pinned_and_failed_downloads_preserve_existing_binary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            commands = root / "commands"
            commands.mkdir()
            # Mock only the network and platform; exercise real tar, hashing, and installation.
            scripts = {
                "uname": '#!/bin/sh\ncase "$1" in -s) echo "$TEST_OS";; -m) echo "$TEST_ARCH";; esac\n',
                "curl": '''#!/bin/sh
set -eu
output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output=$2; shift;;
    https://*) url=$1;;
  esac
  shift
done
case "$url" in
  */releases/latest) printf 'https://github.com/loomx-ai/steward/releases/tag/v1.2.3';;
  */releases/download/v1.2.3/*) cp "$TEST_RELEASE/${url##*/}" "$output";;
  *) exit 22;;
esac
''',
            }
            for name, content in scripts.items():
                command = commands / name
                command.write_text(content)
                command.chmod(0o755)
            output = root / "release"
            output.mkdir()
            binary = root / "steward"
            binary.write_bytes(b"verified executable")
            install_dir = root / "directory with spaces"
            env = dict(os.environ, PATH=f"{commands}{os.pathsep}{os.environ['PATH']}", TEST_RELEASE=str(output), STEWARD_INSTALL_DIR=str(install_dir))

            def install(version=None):
                return subprocess.run(["sh", str(ROOT / "install.sh")] + ([version] if version else []), env=env, capture_output=True, text=True)

            for os_name, arch, target in (("Darwin", "arm64", "darwin_arm64"), ("Darwin", "x86_64", "darwin_amd64"), ("Linux", "x86_64", "linux_amd64"), ("Linux", "aarch64", "linux_arm64")):
                with self.subTest(target=target):
                    env.update(TEST_OS=os_name, TEST_ARCH=arch)
                    name = f"steward_1.2.3_{target}.tar.gz"
                    archive = output / name
                    with tarfile.open(archive, "w:gz") as bundle:
                        bundle.add(binary, arcname="steward")
                    checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
                    (output / "checksums.txt").write_text(f"{checksum}  {name}\n")
                    for version in (None, "v1.2.3", "1.2.3"):
                        result = install(version)
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertEqual((install_dir / "steward").read_bytes(), binary.read_bytes())
                        self.assertTrue(os.access(install_dir / "steward", os.X_OK))
                    for invalid_checksums in ("", f"{'0' * 64}  {name}\n", f"{checksum}  wrong-name.tar.gz\n"):
                        (output / "checksums.txt").write_text(invalid_checksums)
                        (install_dir / "steward").write_bytes(b"existing install")
                        result = install("1.2.3")
                        self.assertNotEqual(result.returncode, 0)
                        self.assertEqual((install_dir / "steward").read_bytes(), b"existing install")
                    archive.unlink()
                    self.assertNotEqual(install("1.2.3").returncode, 0)
            for invalid in ("../escape", "1.2.3\n../../escape", "1.2", "1.2.3;id"):
                self.assertNotEqual(install(invalid).returncode, 0)
            env["TEST_ARCH"] = "i686"
            self.assertNotEqual(install("1.2.3").returncode, 0)
            self.assertEqual((install_dir / "steward").read_bytes(), b"existing install")


if __name__ == "__main__":
    unittest.main()
