#!/usr/bin/env python3
"""Workspace quality check — GitOps compliance, Bazel targets, file tree."""

import sys
import tarfile
from pathlib import Path

SESSION_ROOT = Path(".telos/sessions")


def find_workspace(session_id: str) -> Path | None:
    session_dir = SESSION_ROOT / session_id
    if not session_dir.exists():
        matches = [d for d in SESSION_ROOT.iterdir() if d.name.startswith(session_id)]
        if len(matches) == 1:
            session_dir = matches[0]
        else:
            return None

    for spec_dir in sorted(session_dir.glob("specs/*")):
        ws = spec_dir / "workspace.tar.gz"
        if ws.exists():
            return ws
    return None


def analyze_workspace(ws_path: Path):
    print(f"Workspace: {ws_path}")
    print(f"Size: {ws_path.stat().st_size / 1024:.0f} KB")
    print()

    with tarfile.open(ws_path) as tf:
        names = sorted(tf.getnames())

        # File counts
        k8s_manifests = [
            n for n in names if "/k8s/" in n and n.endswith((".yaml", ".yml"))
        ]
        bazel_files = [
            n for n in names if n.endswith("BUILD.bazel") or n.endswith("BUILD")
        ]
        test_files = [
            n
            for n in names
            if "/tests/" in n and (n.endswith(".py") or n.endswith(".sh"))
        ]
        spec_files = [n for n in names if n.endswith("spec.md")]
        inject_files = [n for n in names if n.endswith("inject.sh")]
        solve_files = [n for n in names if n.endswith("solve.sh")]
        md_files = [
            n for n in names if n.endswith(".md") and not n.startswith("./.agents/")
        ]
        service_dirs = set()
        for n in names:
            parts = n.split("/")
            if len(parts) >= 3 and parts[1] == "services":
                service_dirs.add(parts[2])

        print(f"Total files: {len(names)}")
        print(f"Services: {sorted(service_dirs)}")
        print(f"K8s manifests: {len(k8s_manifests)}")
        print(f"Bazel BUILD files: {len(bazel_files)}")
        print(f"Test files: {len(test_files)}")
        print(f"Spec files: {len(spec_files)}")
        print()

        # GitOps check
        print("GitOps compliance:")
        if k8s_manifests:
            print(
                f"  K8s manifests in services/*/k8s/: YES ({len(k8s_manifests)} files)"
            )
            for m in k8s_manifests[:8]:
                print(f"    {m}")
            if len(k8s_manifests) > 8:
                print(f"    ... +{len(k8s_manifests) - 8} more")
        else:
            print("  K8s manifests in services/*/k8s/: NO")
        print()

        task_public_specs = [n for n in names if n.endswith("public/spec.md")]

        # Task package check
        if task_public_specs or inject_files or solve_files:
            print("Task package:")
            if task_public_specs:
                print(f"  public/spec.md: {task_public_specs}")
            print(f"  inject.sh: {inject_files}")
            print(f"  solve.sh: {solve_files}")
            print(f"  spec.md: {spec_files}")
            for tf_name in test_files[:5]:
                print(f"  test: {tf_name}")
            print()

        # Bazel check
        if bazel_files:
            print(f"Bazel targets: {len(bazel_files)} BUILD files")
            for b in bazel_files[:5]:
                print(f"    {b}")
        else:
            print("Bazel targets: NONE")
        print()

        # Git log (if .telos/evidence exists in tarball)
        print("Documentation:")
        for m in md_files[:10]:
            print(f"  {m}")


def main():
    if len(sys.argv) < 2:
        print("Usage: workspace.py <session_id or prefix>")
        return

    ws = find_workspace(sys.argv[1])
    if ws is None:
        print(f"No workspace found for {sys.argv[1]}")
        return
    analyze_workspace(ws)


if __name__ == "__main__":
    main()
