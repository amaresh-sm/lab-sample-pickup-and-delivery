import json
import importlib.util
import tempfile
import unittest
from pathlib import Path


ADAPTER_PATH = Path(__file__).resolve().parents[1] / "verifier/score_adapter.py"
SPEC = importlib.util.spec_from_file_location("data_consent_score_adapter", ADAPTER_PATH)
assert SPEC and SPEC.loader
score_adapter = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(score_adapter)


class DataConsentAdapterTests(unittest.TestCase):
    """Keep the task evidence-to-score boundary executable without Docker."""

    def test_maps_fractional_component_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            backend = root / "backend.json"
            ui = root / "ui.json"
            output = root / "criteria.json"
            backend.write_text(
                json.dumps(
                    {
                        "contractArtifactParityScore": 1.0,
                        "runtimeContractConformanceScore": 0.75,
                        "backendFunctionalScore": 0.5,
                        "backendNonFunctionalScore": 0.25,
                    }
                )
            )
            ui.write_text(json.dumps({"overall": 0.6, "contract_audit": {"score": 1.0}}))
            status = score_adapter.run(
                type("Args", (), {"backend": backend, "ui": ui, "output": output})()
            )
            self.assertEqual(status, 0)
            report = json.loads(output.read_text())
            self.assertEqual(report["criteria"]["backend-behavior"]["score"], 0.5)
            self.assertEqual(report["criteria"]["reliability"]["status"], "partial")

    def test_missing_component_is_blocked(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            backend = root / "backend.json"
            ui = root / "ui.json"
            output = root / "criteria.json"
            backend.write_text("{}")
            ui.write_text("{}")
            score_adapter.run(type("Args", (), {"backend": backend, "ui": ui, "output": output})())
            report = json.loads(output.read_text())
            self.assertEqual(report["criteria"]["ui-behavior"]["status"], "blocked")
            self.assertEqual(report["criteria"]["ui-behavior"]["score"], 0.0)


if __name__ == "__main__":
    unittest.main()
