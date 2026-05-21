import unittest

from integrations.harbor.telos_agent import (
    is_completed_telos_session,
    parse_marked_json,
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

    def test_is_completed_telos_session(self):
        self.assertTrue(is_completed_telos_session({"status": "completed"}))
        self.assertFalse(is_completed_telos_session({"status": "failed"}))
        self.assertFalse(is_completed_telos_session({}))


if __name__ == "__main__":
    unittest.main()
