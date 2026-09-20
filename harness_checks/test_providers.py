import unittest

from astra_harness.providers import build_provider_command


class ProviderCommandTests(unittest.TestCase):
    """Keep each advertised generation provider constructible without Docker."""

    def test_claude_code_reads_the_prompt_from_standard_input(self) -> None:
        command = build_provider_command("claude-code", "claude-opus-4-8", "max")

        self.assertIn("claude -p", command.command)
        self.assertIn("--model claude-opus-4-8", command.command)
        self.assertIn("--effort max", command.command)
        self.assertIn("--output-format stream-json", command.command)
        self.assertNotIn("{prompt}", command.command)

    def test_all_advertised_providers_construct_a_command(self) -> None:
        for provider in ("codex", "openai-compatible", "claude-code", "openhands", "pi-dev"):
            with self.subTest(provider=provider):
                command = build_provider_command(provider, "example-model", "high")
                self.assertTrue(command.command)

    def test_openhands_uses_the_gateway_runner_and_private_output_directory(self) -> None:
        command = build_provider_command("openhands", "gpt-5.6-sol", "medium")

        self.assertIn("hackerrank-openhands run", command.command)
        self.assertIn("--instruction-file /workspace/INSTRUCTION.md", command.command)
        self.assertIn("--output /tmp/openhands-run", command.command)
        self.assertIn("ASTRA_GATEWAY_API_KEY", command.credential_env)

    def test_codex_paths_request_structured_events(self) -> None:
        for provider in ("codex", "openai-compatible"):
            with self.subTest(provider=provider):
                command = build_provider_command(provider, "example-model", "high")
                self.assertIn("--json", command.command)

    def test_pi_dev_uses_gateway_model_profile_and_instruction_file(self) -> None:
        command = build_provider_command("pi-dev", "claude-sonnet-5", "high")

        self.assertIn("/home/agent/.pi/agent/models.json", command.command)
        self.assertIn("pi --model hackerrank/claude-sonnet-5", command.command)
        self.assertIn("--thinking high", command.command)
        self.assertIn("-p @/workspace/INSTRUCTION.md", command.command)
        self.assertIn("ASTRA_GATEWAY_API_KEY", command.credential_env)


if __name__ == "__main__":
    unittest.main()
