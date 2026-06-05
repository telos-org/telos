import inspect
import unittest

from integrations.harbor.telos_agent import (
    TelosExecutableAgent,
    is_completed_telos_session,
    parse_marked_json,
    parse_marked_text,
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
            skills=("verify-engineering*",),
        )

        self.assertIn("platform: local", rendered)
        self.assertIn("name: scbench-circuit-eval", rendered)
        self.assertIn("skills:\n  - verify-engineering*", rendered)
        self.assertIn("# Spec", rendered)
        self.assertIn("Harbor's task working directory is `/app`", rendered)
        self.assertIn("official Harbor benchmark verifier", rendered)
        self.assertIn("child sessions", rendered)
        self.assertIn("candidate", rendered)
        self.assertNotIn("telos-pvg", rendered)

    def test_sanitize_spec_name(self):
        self.assertEqual(sanitize_spec_name("SCBench: Circuit Eval"), "scbench-circuit-eval")
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

    def test_run_script_preserves_raw_logs_and_fails_non_completed_sessions(self):
        agent = object.__new__(TelosExecutableAgent)
        agent.model_name = "openai-codex/gpt-5.5"
        agent.thinking = "high"
        agent.until = 3
        agent.max_cost_usd = 10
        agent.agent_timeout_sec = 0
        agent.session_timeout_sec = 3600
        agent.poll_interval_sec = 5
        script = agent._run_script("---\nversion: v0\nname: task\n---\nBody", "/app")

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


if __name__ == "__main__":
    unittest.main()
