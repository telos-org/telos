import hashlib
import io
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
import unittest


VERSION = "v0.1.5+master.abc123"
INSTALLER = Path(__file__).with_name("install-release.sh")


class InstallReleaseTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="telos release's ")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.release = self.root / "releases" / VERSION
        self.release.mkdir(parents=True)
        self.binaries = self.root / "bin"
        self.skills = self.root / "custom skills"
        system = platform.system().lower()
        arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(
            platform.machine(), platform.machine()
        )
        self.cli_artifact = f"telos-{system}-{arch}"
        self.daemon_artifact = f"telosd-{system}-{arch}"
        for artifact in (self.cli_artifact, self.daemon_artifact):
            (self.release / artifact).write_bytes(f"released {artifact}".encode())
        self.write_skill()
        self.write_checksums()
        self.installer = self.root / "install.sh"
        self.installer.write_text(
            INSTALLER.read_text()
            .replace("@TELOS_VERSION@", VERSION)
            .replace("@TELOS_SKILL_VERSION@", VERSION[1:])
        )

    def write_skill(self, name="SKILL.md"):
        with tarfile.open(self.release / "telos-cli-skill.tar.gz", "w:gz") as archive:
            data = b"released skill"
            entry = tarfile.TarInfo(name)
            entry.size = len(data)
            entry.mode = 0o644
            archive.addfile(entry, io.BytesIO(data))

    def write_checksums(self):
        files = (
            self.cli_artifact,
            self.daemon_artifact,
            "telos-cli-skill.tar.gz",
        )
        (self.release / "SHA256SUMS").write_text(
            "".join(
                f"{hashlib.sha256((self.release / name).read_bytes()).hexdigest()}  {name}\n"
                for name in files
            )
        )

    def install(self, local=False, remember_skills=False, extra_env=None, args=()):
        env = dict(os.environ)
        env["TELOS_INSTALL_DIR"] = str(self.binaries)
        env["TELOS_RELEASE_BASE_URL"] = (self.root / "releases").as_uri()
        if remember_skills:
            env.pop("TELOS_AGENT_SKILLS_DIR", None)
        else:
            env["TELOS_AGENT_SKILLS_DIR"] = str(self.skills)
        env.update(extra_env or {})
        command = ["sh", str(self.installer), *args]
        if local:
            command.append("--with-telosd")
        return subprocess.run(
            command,
            env=env,
            text=True,
            capture_output=True,
            check=False,
        )

    def assert_installed(self, local=False):
        self.assertEqual(
            (self.binaries / "telos").read_bytes(),
            (self.release / self.cli_artifact).read_bytes(),
        )
        self.assertEqual((self.binaries / "telos").stat().st_mode & 0o777, 0o755)
        self.assertEqual((self.skills / "telos-cli" / "SKILL.md").read_bytes(), b"released skill")
        self.assertEqual(
            (self.binaries / ".telos-skill-path").read_text(),
            str(self.skills / "telos-cli") + "\n",
        )
        if local:
            self.assertEqual(
                (self.binaries / "telosd").read_bytes(),
                (self.release / self.daemon_artifact).read_bytes(),
            )
        else:
            self.assertFalse((self.binaries / "telosd").exists())
        self.assertEqual(list(self.binaries.glob(".telos-install.*")), [])
        self.assertEqual(list(self.skills.glob(".telos-cli.*")), [])

    def test_default_installs_cloud_client_without_fetching_daemon(self):
        (self.release / self.daemon_artifact).unlink()
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_installed()
        self.assertNotIn("pi", result.stdout)

    def test_local_runtime_is_explicit(self):
        result = self.install(local=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_installed(local=True)

    def test_help_and_unknown_options_do_not_install(self):
        for option in ("--help", "-h", "--with-telos"):
            with self.subTest(option=option):
                result = self.install(args=(option,))
                if option == "--with-telos":
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("unknown option", result.stderr)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("--with-telosd", result.stdout)
                    self.assertIn("TELOS_INSTALL_DIR", result.stdout)
                self.assertFalse(self.binaries.exists())
                self.assertFalse(self.skills.exists())

    def test_piped_installer_accepts_runtime_flag(self):
        env = dict(os.environ)
        env["TELOS_INSTALL_DIR"] = str(self.binaries)
        env["TELOS_AGENT_SKILLS_DIR"] = str(self.skills)
        env["TELOS_RELEASE_BASE_URL"] = (self.root / "releases").as_uri()
        result = subprocess.run(
            ["sh", "-s", "--", "--with-telosd"],
            input=self.installer.read_text(),
            env=env,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_installed(local=True)

    def test_default_locations_are_under_home(self):
        home = self.root / "home"
        result = self.install(
            extra_env={
                "HOME": str(home),
                "TELOS_INSTALL_DIR": "",
                "TELOS_AGENT_SKILLS_DIR": "",
            }
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.binaries = home / ".local/bin"
        self.skills = home / ".agents/skills"
        self.assert_installed()

    def test_reinstall_preserves_existing_local_runtime_and_custom_skill_path(self):
        self.assertEqual(self.install(local=True).returncode, 0)
        (self.binaries / "telosd").write_bytes(b"older runtime")
        (self.skills / "telos-cli" / "obsolete.md").write_text("old reference")
        result = self.install(remember_skills=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_installed(local=True)
        self.assertFalse((self.skills / "telos-cli" / "obsolete.md").exists())

    def test_bad_skill_checksum_keeps_existing_installation(self):
        self.assertEqual(self.install(local=True).returncode, 0)
        (self.binaries / "telos").write_bytes(b"old CLI")
        (self.binaries / "telosd").write_bytes(b"old runtime")
        (self.release / "telos-cli-skill.tar.gz").write_bytes(b"corrupted skill")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum verification failed", result.stderr)
        self.assertEqual((self.binaries / "telos").read_bytes(), b"old CLI")
        self.assertEqual((self.binaries / "telosd").read_bytes(), b"old runtime")
        self.assertEqual((self.skills / "telos-cli" / "SKILL.md").read_bytes(), b"released skill")

    def test_incomplete_skill_is_rejected_before_binaries_change(self):
        self.binaries.mkdir()
        (self.binaries / "telos").write_bytes(b"old CLI")
        (self.binaries / "telosd").write_bytes(b"old runtime")
        self.write_skill(name="missing.md")
        self.write_checksums()
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("missing SKILL.md", result.stderr)
        self.assertEqual((self.binaries / "telos").read_bytes(), b"old CLI")
        self.assertEqual((self.binaries / "telosd").read_bytes(), b"old runtime")
        self.assertFalse((self.skills / "telos-cli").exists())
        self.assertEqual(list(self.binaries.glob(".telos-install.*")), [])
        self.assertEqual(list(self.skills.glob(".telos-cli.*")), [])

    def test_replacement_failures_restore_existing_installation(self):
        self.assertEqual(self.install(local=True).returncode, 0)
        tools = self.root / "tools"
        tools.mkdir()
        move = tools / "mv"
        move.write_text(
            "#!/bin/sh\n"
            'if [ "$1" = -f ]; then shift; fi\n'
            'if [ "$INSTALL_TEST_FAILURE" = skill-backup ] && '
            '[ "$1" = "$INSTALL_TEST_SKILL_TARGET" ]; then exit 73; fi\n'
            'if [ "$INSTALL_TEST_FAILURE" = cli-replacement ] && '
            '[ "${1##*/}" = telos ] && [ "$2" = "$INSTALL_TEST_CLI_TARGET" ]; then exit 73; fi\n'
            'if [ "$INSTALL_TEST_FAILURE" = daemon-replacement ] && '
            '[ "${1##*/}" = telosd ] && [ "$2" = "$INSTALL_TEST_DAEMON_TARGET" ]; then exit 73; fi\n'
            'exec /bin/mv "$@"\n'
        )
        move.chmod(0o755)
        for failure in ("skill-backup", "cli-replacement", "daemon-replacement"):
            with self.subTest(failure=failure):
                (self.binaries / "telos").write_bytes(b"old CLI")
                (self.binaries / "telosd").write_bytes(b"old runtime")
                (self.skills / "telos-cli" / "SKILL.md").write_bytes(b"old skill")
                result = self.install(
                    extra_env={
                        "PATH": str(tools) + os.pathsep + os.environ["PATH"],
                        "INSTALL_TEST_FAILURE": failure,
                        "INSTALL_TEST_SKILL_TARGET": str(self.skills / "telos-cli"),
                        "INSTALL_TEST_CLI_TARGET": str(self.binaries / "telos"),
                        "INSTALL_TEST_DAEMON_TARGET": str(self.binaries / "telosd"),
                    }
                )
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual((self.binaries / "telos").read_bytes(), b"old CLI")
                self.assertEqual((self.binaries / "telosd").read_bytes(), b"old runtime")
                self.assertEqual((self.skills / "telos-cli" / "SKILL.md").read_bytes(), b"old skill")
                self.assertEqual(
                    (self.binaries / ".telos-skill-path").read_text(),
                    str(self.skills / "telos-cli") + "\n",
                )
                self.assertEqual(list(self.binaries.glob(".telos-install.*")), [])
                self.assertEqual(list(self.skills.glob(".telos-cli.*")), [])

        shutil.rmtree(self.binaries)
        shutil.rmtree(self.skills)
        result = self.install(
            local=True,
            extra_env={
                "PATH": str(tools) + os.pathsep + os.environ["PATH"],
                "INSTALL_TEST_FAILURE": "daemon-replacement",
                "INSTALL_TEST_SKILL_TARGET": str(self.skills / "telos-cli"),
                "INSTALL_TEST_CLI_TARGET": str(self.binaries / "telos"),
                "INSTALL_TEST_DAEMON_TARGET": str(self.binaries / "telosd"),
            },
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(list(self.binaries.iterdir()), [])
        self.assertEqual(list(self.skills.iterdir()), [])

    def test_release_builder_keeps_both_binaries_for_hosted_bootstrap(self):
        repository = self.root / "repo"
        scripts = repository / "scripts"
        scripts.mkdir(parents=True)
        for name in ("build-release.sh", "install-release.sh"):
            shutil.copyfile(INSTALLER.with_name(name), scripts / name)
        outputs = self.root / "build outputs"
        outputs.mkdir()
        artifacts = [
            f"{binary}-{system}-{arch}"
            for binary in ("telos", "telosd")
            for system in ("darwin", "linux")
            for arch in ("amd64", "arm64")
        ]
        for artifact in artifacts:
            (outputs / artifact).write_bytes(artifact.encode())
        shutil.copyfile(self.release / "telos-cli-skill.tar.gz", outputs / "telos-cli-skill.tar.gz")
        tools = self.root / "build tools"
        tools.mkdir()
        bazel = tools / "bazel"
        bazel.write_text(
            "#!/bin/sh\n"
            'if [ "$1" = build ]; then exit 0; fi\n'
            'for arg in "$@"; do label="$arg"; done\n'
            'case "$label" in\n'
            '  //skills:telos_cli_bundle) artifact=telos-cli-skill.tar.gz ;;\n'
            '  *) artifact="$(printf %s "${label##*:}" | tr _ -)" ;;\n'
            'esac\n'
            'printf "%s/%s\\n" "$INSTALL_TEST_BUILD_OUTPUTS" "$artifact"\n'
        )
        bazel.chmod(0o755)
        env = dict(os.environ)
        env["PATH"] = str(tools) + os.pathsep + env["PATH"]
        env["INSTALL_TEST_BUILD_OUTPUTS"] = str(outputs)
        env.pop("TELOS_DARWIN_CODESIGN_IDENTITY", None)
        result = subprocess.run(
            ["bash", str(scripts / "build-release.sh"), VERSION],
            env=env,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        release = repository / "dist" / VERSION
        manifest = json.loads((release / "manifest.json").read_text())
        self.assertEqual(manifest["version"], VERSION)
        self.assertEqual(len(manifest["platforms"]), 4)
        for entry in manifest["platforms"]:
            suffix = f"{entry['os']}-{entry['arch']}"
            self.assertEqual(entry["telos"], f"telos-{suffix}")
            self.assertEqual(entry["telosd"], f"telosd-{suffix}")
        checksums = (release / "SHA256SUMS").read_text()
        for artifact in artifacts + ["telos-cli-skill.tar.gz"]:
            digest = hashlib.sha256((release / artifact).read_bytes()).hexdigest()
            self.assertIn(f"{digest}  {artifact}\n", checksums)
        self.assertNotIn("@TELOS_", (release / "install.sh").read_text())


if __name__ == "__main__":
    unittest.main()
