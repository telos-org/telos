import asyncio
import hashlib
import inspect
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

from integrations.harbor.telos_agent import (
    TelosExecutableAgent,
    is_completed_telos_session,
    is_countable_telos_session,
    parse_marked_json,
    parse_marked_text,
    raise_for_failed_run,
    render_harbor_spec,
    sanitize_spec_name,
    split_skills,
)


class TelosHarborAgentTest(unittest.TestCase):
    def test_render_harbor_spec_is_local_telos_spec(self):
        rendered = render_harbor_spec(
            "Implement checkpoint 1.",
            workdir="/app",
            name="SCBench: Circuit Eval",
            skills=("@telos/verify-engineering:0.1.0*",),
        )

        self.assertIn("platform: local", rendered)
        self.assertIn("name: scbench-circuit-eval", rendered)
        self.assertIn(
            "skills:\n  - @telos/verify-engineering:0.1.0*", rendered
        )
        self.assertIn("# Spec", rendered)
        self.assertIn("Harbor's task working directory is `/app`", rendered)
        self.assertIn("official Harbor benchmark verifier", rendered)
        self.assertIn("child sessions", rendered)
        self.assertIn("candidate", rendered)
        self.assertNotIn("telos-pvg", rendered)

    def test_sanitize_spec_name(self):
        self.assertEqual(
            sanitize_spec_name("SCBench: Circuit Eval"), "scbench-circuit-eval"
        )
        self.assertEqual(sanitize_spec_name("123"), "task-123")

    def test_split_skills(self):
        self.assertEqual(
            split_skills("verify-engineering*, verify-quality*\nfoo"),
            ("verify-engineering*", "verify-quality*", "foo"),
        )

    def test_parse_marked_json(self):
        parsed = parse_marked_json(
            'noise\nTELOS_HARBOR_RESULT_BEGIN\n{"status":"completed"}\n'
            "TELOS_HARBOR_RESULT_END\n"
        )
        self.assertEqual(parsed["status"], "completed")

    def test_parse_marked_text(self):
        parsed = parse_marked_text(
            "noise\nTELOS_HARBOR_TRANSCRIPT_BEGIN\n# Transcript\n"
            "TELOS_HARBOR_TRANSCRIPT_END\n",
            "TELOS_HARBOR_TRANSCRIPT_BEGIN",
            "TELOS_HARBOR_TRANSCRIPT_END",
        )
        self.assertEqual(parsed, "# Transcript")

    def test_is_completed_telos_session(self):
        self.assertTrue(is_completed_telos_session({"status": "completed"}))
        self.assertFalse(is_completed_telos_session({"status": "failed"}))
        self.assertFalse(is_completed_telos_session({}))

    def test_is_countable_telos_session_accepts_review_budget_exhaustion(self):
        self.assertTrue(is_countable_telos_session({"status": "completed"}))
        self.assertTrue(
            is_countable_telos_session(
                {"status": "failed", "completion_reason": "review_budget_exhausted"}
            )
        )
        self.assertFalse(
            is_countable_telos_session(
                {"status": "failed", "completion_reason": "max_cost_usd_exceeded"}
            )
        )
        self.assertFalse(is_countable_telos_session({}))

    def test_raise_for_failed_run_spares_countable_sessions(self):
        raise_for_failed_run(0, "", {})
        raise_for_failed_run(1, "", {"status": "completed"})
        raise_for_failed_run(
            1, "", {"status": "failed", "completion_reason": "review_budget_exhausted"}
        )
        with self.assertRaises(RuntimeError):
            raise_for_failed_run(
                1,
                "boom",
                {"status": "failed", "completion_reason": "max_cost_usd_exceeded"},
            )
        with self.assertRaises(RuntimeError):
            raise_for_failed_run(1, "boom", {})

    def test_run_script_preserves_raw_logs_and_fails_non_completed_sessions(self):
        agent = object.__new__(TelosExecutableAgent)
        agent.model_name = "openai-codex/gpt-5.5"
        agent.thinking = "high"
        agent.until = 3
        agent.max_cost_usd = 10
        agent.session_timeout_sec = 3600
        agent.poll_interval_sec = 5
        agent.telos_binary_dir = None
        script = agent._run_script("---\nversion: 0.1.0\nname: task\n---\nBody", "/app")

        self.assertIn("--workspace /app", script)
        self.assertIn('telos logs "$session_id" --raw', script)
        self.assertIn("retry() {", script)
        self.assertIn("json_field /tmp/telos-harbor/run.json session_id", script)
        self.assertIn("json_field /tmp/telos-harbor/describe.json status", script)
        self.assertNotIn("awk -F", script)
        self.assertIn("TELOS_HARBOR_TRANSCRIPT_BEGIN", script)
        self.assertIn('if [ "$status" != completed ]; then', script)

    def test_install_scripts_retry_network_fetches(self):
        telos_source = inspect.getsource(TelosExecutableAgent._install_telos)
        pi_source = inspect.getsource(TelosExecutableAgent._install_pi)

        self.assertIn("retry 5 curl -fsSL", telos_source)
        self.assertIn("retry 5 curl -fsSL", pi_source)
        self.assertIn("retry 5 npm install", pi_source)


class TelosLocalBuildTest(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        # Spaces and apostrophes exercise quoting of local and remote paths.
        temporary = tempfile.TemporaryDirectory(prefix="telos build's ")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = self.root / "build"
        self.remote = self.root / "container-bin"
        self.source.mkdir()
        self.remote.mkdir()
        for name in ("telos", "telosd"):
            (self.source / name).write_text(
                f"#!/bin/sh\nprintf '%s\\n' '{name} pinned-test'\n"
            )
            (self.remote / name).write_text("old installation")
        self.agent = TelosExecutableAgent(
            logs_dir=self.root / "logs",
            telos_binary_dir=str(self.source),
        )
        self.environment = SimpleNamespace(
            upload_file=AsyncMock(side_effect=shutil.copyfile)
        )
        self.commands = []
        self.agent.exec_as_agent = AsyncMock(side_effect=self.exec_as_agent)
        self.agent.exec_as_root = AsyncMock(side_effect=self.exec_as_root)

    async def exec_as_agent(self, environment, command, **kwargs):
        self.commands.append(command)
        if len(self.commands) == 1:
            # Stand in for discovering the container's home; execute subsequent
            # install commands for real, only inside our temporary directory.
            return SimpleNamespace(stdout=str(self.remote))
        return await asyncio.to_thread(
            subprocess.run, command, shell=True, capture_output=True, text=True,
            check=True,
        )

    async def exec_as_root(self, environment, command, **kwargs):
        return await asyncio.to_thread(
            subprocess.run, command, shell=True, capture_output=True, text=True,
            check=True,
        )

    async def test_installs_selected_bytes_and_records_versions_and_hashes(self):
        await self.agent._install_telos(self.environment)

        log = (self.agent.logs_dir / "telos-harbor-build.log").read_text()
        for name in ("telos", "telosd"):
            source = (self.source / name).read_bytes()
            self.assertEqual((self.remote / name).read_bytes(), source)
            self.assertTrue(os.access(self.remote / name, os.X_OK))
            self.assertIn(f"{name} pinned-test", log)
            self.assertIn(hashlib.sha256(source).hexdigest(), log)
        self.assertEqual(self.environment.upload_file.await_count, 2)

    async def test_missing_binary_fails_before_upload_or_release_fallback(self):
        (self.source / "telosd").unlink()
        with self.assertRaises(FileNotFoundError):
            await self.agent._install_telos(self.environment)
        self.environment.upload_file.assert_not_awaited()
        self.agent.exec_as_agent.assert_not_awaited()

    async def test_broken_binary_fails_without_release_fallback(self):
        (self.source / "telos").write_text("#!/bin/sh\nexit 9\n")
        with self.assertRaises(subprocess.CalledProcessError):
            await self.agent._install_telos(self.environment)
        self.assertEqual(self.agent.exec_as_agent.await_count, 2)
        self.assertFalse((self.agent.logs_dir / "telos-harbor-build.log").exists())

    def test_cannot_silently_disable_selected_build(self):
        with self.assertRaisesRegex(ValueError, "requires install_telos=true"):
            TelosExecutableAgent(
                logs_dir=self.root / "disabled-logs",
                telos_binary_dir=str(self.source),
                install_telos=False,
            )

    def test_run_script_overrides_inherited_daemon_and_path(self):
        # Shell initialization (such as NVM) can change PATH before the run.
        with patch.object(
            self.agent, "_shell_prologue",
            return_value='set -eu\nexport PATH="/stale-bin:$PATH"',
        ):
            script = self.agent._run_script("task", "/app")
        setup = script.split("mkdir -p /tmp/telos-harbor", 1)[0]
        result = subprocess.run(
            ["bash", "-c", setup + '\nprintf "%s\\n" "$TELOSD_PATH" "$PATH"'],
            env={**os.environ, "TELOSD_PATH": "/stale-bin/telosd"},
            capture_output=True,
            text=True,
            check=True,
        )
        daemon, path = result.stdout.splitlines()
        installed_dir = Path.home() / ".local" / "bin"
        self.assertEqual(daemon, str(installed_dir / "telosd"))
        self.assertEqual(path.split(":", 1)[0], str(installed_dir))


if __name__ == "__main__":
    unittest.main()
