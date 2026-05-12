#!/usr/bin/env python3
"""Frontier view for an adversary controller.

Given a parent session id, list the most recent non-controller task
sessions with the signal that actually matters for targeting: round
count, verifier pushback count, cost, checkpoint presence, result.

Usage:
    frontier.py --parent sess_<id> [--limit N] [--json]

The verifier-pushback count is the alpha signal: a task session that
ran 2 rounds with 0 pushbacks is p1v1 (cheap success); a task that
ran 8 rounds with 3 pushbacks is frontier-level (real disagreement).
The adversary's job is to push cheap cases into the frontier.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path


def _sessions_root() -> Path:
    env = os.environ.get("TELOS_SESSION_DIR")
    if env:
        return Path(env)
    for candidate in (Path("/telos-state/sessions"), Path(".telos/sessions")):
        if candidate.is_dir():
            return candidate
    return Path(".telos/sessions")


def _child_summary(session_dir: Path) -> dict | None:
    manifest_path = session_dir / "session.json"
    if not manifest_path.is_file():
        return None
    try:
        manifest = json.loads(manifest_path.read_text())
    except (json.JSONDecodeError, OSError):
        return None

    max_round = 0
    verifier_continues = 0
    verifier_concede_round: int | None = None
    cost = 0.0
    terminal_result: str | None = None
    finished_at: str | None = None
    for spec in manifest.get("specs", []) or []:
        spec_dir = session_dir / "specs" / spec["dir_name"]
        ev_path = spec_dir / "evidence.jsonl"
        if not ev_path.is_file():
            continue
        try:
            for line in ev_path.read_text().splitlines():
                if not line.strip():
                    continue
                event = json.loads(line)
                event_type = event.get("event")
                round_num = event.get("round", 0)
                if event_type == "agent_complete":
                    max_round = max(max_round, round_num)
                    data = event.get("data") or {}
                    cost += float(data.get("cost_usd") or 0.0)
                    if event.get("role") == "verifier":
                        status = data.get("status")
                        if status == "CONTINUE":
                            verifier_continues += 1
                        elif status == "CONCEDE" and verifier_concede_round is None:
                            verifier_concede_round = round_num
                elif event_type == "game_end":
                    terminal_result = (event.get("data") or {}).get("game_result")
                    finished_at = event.get("ts")
        except (OSError, json.JSONDecodeError):
            continue

    checkpoint_exists = False
    for spec in manifest.get("specs", []) or []:
        wp = spec.get("workspace_path")
        if wp and Path(wp).is_file():
            checkpoint_exists = True
            break

    epochs = manifest.get("epochs") or []
    last_epoch = epochs[-1] if epochs else None
    status: str
    if last_epoch is None:
        status = "pending"
    elif last_epoch.get("finished_at") is None:
        status = "running"
    else:
        status = str(last_epoch.get("result") or "completed")

    return {
        "session_id": session_dir.name,
        "name": manifest.get("spec_name", ""),
        "status": status,
        "result": terminal_result,
        "cost": cost,
        "rounds": max_round,
        "verifier_pushbacks": verifier_continues,
        "concede_round": verifier_concede_round,
        "checkpoint": checkpoint_exists,
        "created_at": manifest.get("created_at", ""),
        "finished_at": finished_at,
        "launcher": manifest.get("launcher", ""),
    }


def _parent_and_name(session_dir: Path) -> tuple[str | None, str]:
    manifest_path = session_dir / "session.json"
    if not manifest_path.is_file():
        return None, ""
    try:
        manifest = json.loads(manifest_path.read_text())
    except (json.JSONDecodeError, OSError):
        return None, ""
    return manifest.get("parent_session_id"), manifest.get("spec_name", "")


def _build_descendant_set(
    root: Path, parent_id: str, exclude_spec_name: str
) -> set[str]:
    """Transitively collect descendant session ids, skipping same-name
    controller cycles (which are the controller firing itself).
    """
    by_parent: dict[str, list[tuple[str, str]]] = {}
    for sd in root.iterdir():
        if not sd.is_dir():
            continue
        parent, name = _parent_and_name(sd)
        if parent is None:
            continue
        by_parent.setdefault(parent, []).append((sd.name, name))

    result: set[str] = set()
    stack = [parent_id]
    while stack:
        current = stack.pop()
        for sid, name in by_parent.get(current, []):
            if sid in result:
                continue
            if name != exclude_spec_name:
                result.add(sid)
            stack.append(sid)
    return result


def _classify(child: dict) -> str:
    result = child.get("result")
    rounds = child.get("rounds", 0)
    pushbacks = child.get("verifier_pushbacks", 0)
    if result == "failure":
        return "failing"
    if result != "success":
        return "pending"
    if pushbacks >= 2 or rounds >= 5:
        return "frontier"
    if pushbacks >= 1 or rounds >= 3:
        return "deepening"
    return "p1v1"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--parent", required=True, help="parent session id")
    parser.add_argument(
        "--limit", type=int, default=10, help="max children to list (most recent)"
    )
    parser.add_argument("--json", action="store_true", help="emit JSON")
    args = parser.parse_args()

    root = _sessions_root()
    if not root.is_dir():
        print(f"No sessions root found at {root}", file=sys.stderr)
        return 1

    # Read the parent's own spec_name so we can exclude same-name
    # controller cycles (they're the controller firing itself, not
    # generated work).
    parent_dir = root / args.parent
    _, parent_name = _parent_and_name(parent_dir)

    descendant_ids = _build_descendant_set(root, args.parent, parent_name)

    children = []
    for sid in descendant_ids:
        summary = _child_summary(root / sid)
        if summary is not None:
            children.append(summary)

    children.sort(key=lambda c: c.get("created_at") or "", reverse=True)
    children = children[: args.limit]
    for child in children:
        child["classification"] = _classify(child)

    totals = {
        "count": len(children),
        "by_class": {
            k: sum(1 for c in children if c["classification"] == k)
            for k in ("p1v1", "deepening", "frontier", "failing", "pending")
        },
        "total_cost": round(sum(c["cost"] for c in children), 2),
    }

    if args.json:
        print(
            json.dumps(
                {"parent": args.parent, "totals": totals, "children": children},
                indent=2,
            )
        )
        return 0

    print(f"Frontier for {args.parent}")
    print(
        f"  {totals['count']} children  "
        f"p1v1={totals['by_class']['p1v1']}  "
        f"deepening={totals['by_class']['deepening']}  "
        f"frontier={totals['by_class']['frontier']}  "
        f"failing={totals['by_class']['failing']}  "
        f"pending={totals['by_class']['pending']}  "
        f"cost=${totals['total_cost']}"
    )
    print()
    header = f"  {'name':42} {'class':10} {'rnd':4} {'pbks':5} {'cost':7} {'result':8} {'ckpt':4}"
    print(header)
    print("  " + "-" * (len(header) - 2))
    for c in children:
        ckpt = "yes" if c["checkpoint"] else "-"
        result = str(c.get("result") or c.get("status") or "")
        print(
            f"  {c['name'][:42]:42} {c['classification']:10} "
            f"{c['rounds']:<4} {c['verifier_pushbacks']:<5} "
            f"${c['cost']:<6.2f} {result:8} {ckpt}"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
