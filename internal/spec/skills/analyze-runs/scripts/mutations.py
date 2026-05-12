#!/usr/bin/env python3
"""Task package output quality — check promoted task packages for completeness."""

import json
import tarfile
from pathlib import Path

SESSION_ROOT = Path(".telos/sessions")
PACKAGE_MARKERS = {"public", "setup", "grader", "solution", "manifests", "tests"}


def task_root(path: str) -> str | None:
    parts = path.split("/")
    for i, part in enumerate(parts):
        if part in PACKAGE_MARKERS:
            root = "/".join(parts[:i])
            if root.split("/")[-1:] == ["task"]:
                return root
            return None
    if path.endswith(("spec.md", "inject.sh", "solve.sh")) and len(parts) > 1:
        root = "/".join(parts[:-1])
        if root.split("/")[-1:] == ["task"]:
            return root
    return None


def main():
    if not SESSION_ROOT.exists():
        print("No sessions found.")
        return

    tasks = []

    for session_dir in sorted(SESSION_ROOT.iterdir(), reverse=True):
        manifest_path = session_dir / "session.json"
        if not manifest_path.is_file():
            continue
        try:
            manifest = json.loads(manifest_path.read_text())
        except (json.JSONDecodeError, OSError):
            continue

        for spec in manifest.get("specs", []):
            spec_dir = session_dir / "specs" / spec["dir_name"]
            ws = spec_dir / "workspace.tar.gz"
            if not ws.exists():
                continue

            try:
                with tarfile.open(ws) as tf:
                    names = tf.getnames()

                    # Find task packages in the current layout first.
                    task_dirs = set()
                    for n in names:
                        if n.endswith("public/spec.md"):
                            root = task_root(n)
                            if root:
                                task_dirs.add(root)
                    # Fall back to legacy layouts so old sessions are still visible.
                    for n in names:
                        if n.endswith("inject.sh") or n.endswith("spec.md"):
                            root = task_root(n)
                            if root:
                                task_dirs.add(root)

                    for task_dir in task_dirs:
                        task_files = [n for n in names if n.startswith(task_dir + "/")]
                        has_public_spec = any(
                            n.endswith("public/spec.md") for n in task_files
                        )
                        has_legacy_spec = any(
                            n.endswith("spec.md") and "/public/" not in n
                            for n in task_files
                        )
                        has_spec = has_public_spec or has_legacy_spec
                        has_inject = any(
                            n.endswith("setup/inject.sh") or n.endswith("inject.sh")
                            for n in task_files
                        )
                        has_solve = any(
                            n.endswith("solution/solve.sh") or n.endswith("solve.sh")
                            for n in task_files
                        )
                        has_setup = any("/setup/" in n for n in task_files) or any(
                            "/manifests/" in n for n in task_files
                        )
                        has_namespaces = any(
                            "/setup/namespaces/" in n or "/manifests/" in n
                            for n in task_files
                        )
                        test_files = [
                            n
                            for n in task_files
                            if "/tests/" in n
                            and (n.endswith(".py") or n.endswith(".sh"))
                        ]
                        has_build = any(
                            n.endswith("BUILD.bazel") or n.endswith("BUILD")
                            for n in task_files
                        )

                        # Read spec to check grounding
                        spec_content = ""
                        for n in task_files:
                            if n.endswith("public/spec.md") or n.endswith("spec.md"):
                                f = tf.extractfile(n)
                                if f:
                                    spec_content = f.read().decode()[:2000]
                                    if n.endswith("public/spec.md"):
                                        break

                        has_frontmatter = spec_content.lstrip().startswith("---")
                        grounded = "$TASK_NAMESPACE" in spec_content
                        has_diagnosis_req = "diagnosis.md" in spec_content
                        has_staged = (
                            "stage" in spec_content.lower()
                            or "staged" in spec_content.lower()
                        )

                        task_name = task_dir.split("/")[-1]
                        quality_score = sum(
                            [
                                has_spec,
                                has_setup,
                                has_solve,
                                has_frontmatter,
                                has_namespaces,
                                len(test_files) >= 3,
                                has_build,
                                grounded,
                                has_diagnosis_req,
                                has_staged,
                            ]
                        )

                        tasks.append(
                            {
                                "session": session_dir.name,
                                "source": spec["name"],
                                "task": task_name,
                                "quality": f"{quality_score}/10",
                                "spec": has_spec,
                                "inject": has_inject,
                                "solve": has_solve,
                                "frontmatter": has_frontmatter,
                                "setup": has_setup,
                                "tests": len(test_files),
                                "build": has_build,
                                "grounded": grounded,
                                "diagnosis": has_diagnosis_req,
                                "staged": has_staged,
                            }
                        )
            except (tarfile.TarError, OSError):
                continue

    if not tasks:
        print("No task packages found.")
        return

    print(f"Task packages: {len(tasks)}")
    print(
        f"{'TASK':<40} {'QUALITY':<10} {'TESTS':<8} {'GROUNDED':<10} {'DIAGNOSIS':<10} {'STAGED'}"
    )
    print("-" * 90)
    for m in tasks:
        print(
            f"{m['task']:<40} {m['quality']:<10} {m['tests']:<8} {str(m['grounded']):<10} {str(m['diagnosis']):<10} {m['staged']}"
        )

    # Summary
    avg_quality = sum(int(m["quality"].split("/")[0]) for m in tasks) / len(tasks)
    grounded_pct = sum(1 for m in tasks if m["grounded"]) / len(tasks) * 100
    print(f"\nAvg quality: {avg_quality:.1f}/10  Grounded: {grounded_pct:.0f}%")


if __name__ == "__main__":
    main()
