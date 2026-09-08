"""Static boundary checks for the two-container verification design."""

from pathlib import Path
from argparse import Namespace
from tempfile import TemporaryDirectory
import unittest

from astra_harness.verify import docker_environment_args, load_candidate_manifest, verifier_environment


ROOT = Path(__file__).resolve().parents[1]


class VerifierBoundaryTests(unittest.TestCase):
    def test_admin_and_timer_environment_is_shared_with_both_containers(self) -> None:
        with TemporaryDirectory() as directory:
            env_file = Path(directory) / "verifier.env"
            env_file.write_text(
                "ADMIN_USERNAME=task-admin\n"
                "ADMIN_PASSWORD='task-password'\n"
                "UNASSIGNED_SEARCH_TIMEOUT_SECONDS=90\n",
                encoding="utf-8",
            )
            values = verifier_environment(
                Namespace(
                    env_file=env_file,
                    candidate_env_file=None,
                    admin_user=None,
                    admin_pass=None,
                    unassigned_search_timeout_seconds=None,
                    assignment_offer_ttl_seconds=180,
                    maintenance_interval_milliseconds=None,
                )
            )
        self.assertEqual(values["ADMIN_USERNAME"], "task-admin")
        self.assertEqual(values["ADMIN_PASSWORD"], "task-password")
        self.assertEqual(values["UNASSIGNED_SEARCH_TIMEOUT_SECONDS"], "90")
        self.assertEqual(values["ASSIGNMENT_OFFER_TTL_SECONDS"], "180")
        self.assertEqual(values["MAINTENANCE_INTERVAL_MILLISECONDS"], "1000")
        self.assertEqual(values["ENGINE_MAINTENANCE_INTERVAL_SECONDS"], "1")
        self.assertEqual(docker_environment_args({"ADMIN_USERNAME": "task-admin"}), ["--env", "ADMIN_USERNAME=task-admin"])

    def test_candidate_manifest_is_app_setup_only(self) -> None:
        with TemporaryDirectory() as directory:
            candidate = Path(directory)
            (candidate / "legacy-layout").mkdir()
            (candidate / "legacy-layout/manifest.json").write_text("{}", encoding="utf-8")
            with self.assertRaises(SystemExit):
                load_candidate_manifest(candidate)

    def test_candidate_runtime_never_receives_private_verifier_mount(self) -> None:
        source = (ROOT / "astra_harness/verify.py").read_text(encoding="utf-8")
        runtime_section = source.split("docker_args =", 1)[0]
        self.assertNotIn("/input/verifier", runtime_section)
        self.assertIn("docker_environment_args(shared_env)", runtime_section)
        self.assertIn("docker_environment_args(shared_env)", source.split("docker_args =", 1)[1])
        self.assertIn('"--network", f"container:{runtime_name}"', source)
        self.assertIn('"--cap-drop", "NET_RAW"', source)

    def test_verifier_entrypoint_never_executes_candidate_commands(self) -> None:
        source = (ROOT / "environment/verifier/entrypoint.sh").read_text(encoding="utf-8")
        self.assertNotIn("/work/candidate", source)
        self.assertIn("cd /input/verifier", source)
        self.assertIn("Candidate lifecycle commands run only", source)


if __name__ == "__main__":
    unittest.main()
