#!/usr/bin/env python3
"""Training signal filter — games with high round depth, verifier findings, fix complexity."""

import json
import re
from pathlib import Path

SESSION_ROOT = Path(".telos/sessions")


def main():
    if not SESSION_ROOT.exists():
        print("No sessions found.")
        return

    high_signal = []

    for session_dir in sorted(SESSION_ROOT.iterdir(), reverse=True):
        manifest_path = session_dir / "session.json"
        if not manifest_path.is_file():
            continue
        try:
            manifest = json.loads(manifest_path.read_text())
        except (json.JSONDecodeError, OSError):
            continue

        config = manifest.get("config", {})
        kubeconfig = config.get("kubeconfig")
        cluster = (
            Path(kubeconfig).stem.replace("kubeconfig-", "") if kubeconfig else "hosted"
        )

        for spec in manifest.get("specs", []):
            spec_dir = session_dir / "specs" / spec["dir_name"]
            ev_path = spec_dir / "evidence.jsonl"
            if not ev_path.exists():
                continue

            events = []
            for line in open(ev_path):
                events.append(json.loads(line))

            game_end = [e for e in events if e["event"] == "game_end"]
            if not game_end:
                continue

            gd = game_end[0].get("data", {})
            if gd.get("game_result") != "success":
                continue

            prover_rounds = gd.get("prover_rounds", 0)
            verifier_rounds = gd.get("verifier_rounds", 0)
            total_rounds = prover_rounds + verifier_rounds
            cost = gd.get("total_cost_usd", 0)

            if total_rounds < 3:
                continue  # Low signal — first-try concede

            # Extract verifier findings
            findings = []
            for e in events:
                if e["event"] == "agent_complete" and e.get("role") == "verifier":
                    logs = e.get("data", {}).get("logs_tail", "")
                    updates = re.findall(
                        r"<progress_update>(.*?)</progress_update>", logs
                    )
                    status = re.findall(r"<status>(.*?)</status>", logs)
                    if updates:
                        findings.append(
                            {
                                "round": e["round"],
                                "progress_update": updates[-1][:150],
                                "conceded": status[-1] == "CONCEDE"
                                if status
                                else False,
                            }
                        )

            # Extract prover fixes
            fixes = []
            for e in events:
                if (
                    e["event"] == "agent_complete"
                    and e.get("role") == "prover"
                    and e["round"] > 1
                ):
                    logs = e.get("data", {}).get("logs_tail", "")
                    updates = re.findall(
                        r"<progress_update>(.*?)</progress_update>", logs
                    )
                    if updates:
                        fixes.append(
                            {
                                "round": e["round"],
                                "progress_update": updates[-1][:150],
                            }
                        )

            high_signal.append(
                {
                    "spec": spec["name"],
                    "session": session_dir.name,
                    "cluster": cluster,
                    "rounds": f"{prover_rounds}P/{verifier_rounds}V",
                    "cost": cost,
                    "findings": findings,
                    "fixes": fixes,
                }
            )

    if not high_signal:
        print("No high-signal games found (3+ rounds with success).")
        return

    # Sort by round count descending
    high_signal.sort(key=lambda x: len(x["findings"]), reverse=True)

    print(f"High-signal games (3+ rounds, successful): {len(high_signal)}")
    print("=" * 80)
    for g in high_signal:
        print(f"\n{g['spec']} ({g['rounds']}, ${g['cost']:.2f}, {g['cluster']})")
        for f in g["findings"]:
            prefix = "CONCEDE" if f["conceded"] else "FINDING"
            print(f"  V R{f['round']}: [{prefix}] {f['progress_update']}")
        for f in g["fixes"]:
            print(f"  P R{f['round']}: {f['progress_update']}")


if __name__ == "__main__":
    main()
