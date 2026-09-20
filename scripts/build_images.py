"""Build the isolated candidate-generation and verifier images."""

from __future__ import annotations

import argparse
import subprocess
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[1]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Build ASTRA's isolated Docker images")
    parser.add_argument(
        "--candidate-tag",
        default="astra-candidate-generation:latest",
        help="Tag for the candidate-generation image",
    )
    parser.add_argument(
        "--verifier-tag",
        default="astra-verifier:latest",
        help="Tag for the verifier image",
    )
    parser.add_argument(
        "--runtime-tag",
        default="astra-candidate-runtime:latest",
        help="Tag for the isolated candidate-runtime image",
    )
    return parser.parse_args()


def build(tag: str, dockerfile: Path, *build_args: str) -> None:
    """Build one image from the repository root so Docker COPY paths are stable."""
    subprocess.run(
        [
            "docker",
            "build",
            "--tag",
            tag,
            "--file",
            str(dockerfile),
            *build_args,
            ".",
        ],
        cwd=REPOSITORY_ROOT,
        check=True,
    )


def main() -> int:
    args = parse_args()
    # The original task's environment/Dockerfile is the complete runtime image
    # (Go, Node, MongoDB, PostgreSQL, Redis, Playwright, and map assets). Build
    # it under the ASTRA runtime tag, then layer the generation and verifier
    # roles on top without making the candidate see private verifier files.
    build(args.runtime_tag, REPOSITORY_ROOT / "environment/Dockerfile")
    build(
        args.candidate_tag,
        REPOSITORY_ROOT / "environment/candidate-generation/Dockerfile",
        "--build-arg",
        f"BASE_IMAGE={args.runtime_tag}",
    )
    build(
        args.verifier_tag,
        REPOSITORY_ROOT / "environment/verifier/Dockerfile",
        "--build-arg",
        f"BASE_IMAGE={args.runtime_tag}",
    )
    print(f"candidate image: {args.candidate_tag}")
    print(f"candidate runtime image: {args.runtime_tag}")
    print(f"verifier image: {args.verifier_tag}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
