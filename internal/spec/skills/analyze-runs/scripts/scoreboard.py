#!/usr/bin/env python3
"""Session scoreboard — status, cost, rounds, cluster."""

import json
from pathlib import Path

SESSION_ROOT = Path(".telos/sessions")


def load_sessions():
    if not SESSION_ROOT.exists():
        return []
    sessions = []
    for d in sorted(SESSION_ROOT.iterdir(), reverse=True):
        manifest = d / "session.json"
        if not manifest.is_file():
            continue
        try:
            data = json.loads(manifest.read_text())
        except (json.JSONDecodeError, OSError):
            continue

        specs = data.get("specs", [])
        config = data.get("config", {})
        kubeconfig = config.get("kubeconfig")
        cluster = "hosted"
        if kubeconfig:
            cluster = Path(kubeconfig).stem.replace("kubeconfig-", "")

        # Derive status from evidence
        total_cost = 0.0
        status = "pending"
        result = None
        max_round = 0
        prover_rounds = 0
        verifier_rounds = 0

        for spec in specs:
            spec_dir = d / "specs" / spec["dir_name"]
            ev_path = spec_dir / "evidence.jsonl"
            if not ev_path.exists():
                continue
            for line in open(ev_path):
                e = json.loads(line)
                if e["event"] == "agent_complete":
                    ed = e.get("data", {})
                    total_cost += float(ed.get("cost_usd", 0) or 0)
                elif e["event"] == "game_end":
                    ed = e.get("data", {})
                    result = ed.get("game_result")
                    prover_rounds = ed.get("prover_rounds", 0)
                    verifier_rounds = ed.get("verifier_rounds", 0)
                    status = "completed" if result == "success" else "failed"
                elif e["event"] in ("game_error", "error"):
                    status = "failed"
                    result = "failure"
                if e.get("round", 0) > max_round:
                    max_round = e["round"]
            if status == "pending" and ev_path.stat().st_size > 0:
                status = "running"

        sessions.append(
            {
                "session_id": d.name,
                "spec": data.get("source_spec_path", "").split("/")[-2]
                if "/" in data.get("source_spec_path", "")
                else "",
                "status": status,
                "result": result or "",
                "cost": total_cost,
                "prover_rounds": prover_rounds,
                "verifier_rounds": verifier_rounds,
                "cluster": cluster,
                "created_at": data.get("created_at", "")[:19],
            }
        )
    return sessions


def main():
    sessions = load_sessions()
    if not sessions:
        print("No sessions found.")
        return

    # Dedup: keep latest per (spec, cluster)
    seen = {}
    for s in sessions:
        key = (s["spec"], s["cluster"])
        if key not in seen or s["status"] in ("completed", "running"):
            seen[key] = s

    completed = sum(1 for s in seen.values() if s["status"] == "completed")
    running = sum(1 for s in seen.values() if s["status"] == "running")
    failed = sum(1 for s in seen.values() if s["status"] == "failed")
    total_cost = sum(s["cost"] for s in seen.values())

    print(f"{'SPEC':<25} {'STATUS':<12} {'ROUNDS':<10} {'COST':>8} {'CLUSTER':<10}")
    print("-" * 70)
    for s in sorted(seen.values(), key=lambda x: x["created_at"], reverse=True):
        rounds = (
            f"{s['prover_rounds']}P/{s['verifier_rounds']}V"
            if s["prover_rounds"]
            else ""
        )
        print(
            f"{s['spec']:<25} {s['status']:<12} {rounds:<10} ${s['cost']:>7.2f} {s['cluster']:<10}"
        )

    print("-" * 70)
    print(
        f"Completed: {completed}  Running: {running}  Failed: {failed}  Total cost: ${total_cost:.2f}"
    )


if __name__ == "__main__":
    main()
