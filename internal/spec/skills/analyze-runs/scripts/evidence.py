#!/usr/bin/env python3
"""Round-by-round evidence for a session."""

import json
import re
import sys
from pathlib import Path

SESSION_ROOT = Path(".telos/sessions")


def analyze_session(session_id: str):
    session_dir = SESSION_ROOT / session_id
    manifest_path = session_dir / "session.json"
    if not manifest_path.exists():
        # Try prefix match
        matches = [d for d in SESSION_ROOT.iterdir() if d.name.startswith(session_id)]
        if len(matches) == 1:
            session_dir = matches[0]
            manifest_path = session_dir / "session.json"
        else:
            print(f"Session not found: {session_id}")
            return

    manifest = json.loads(manifest_path.read_text())
    print(f"Session: {session_dir.name}")
    print(f"Source: {manifest.get('source_spec_path', '').split('/')[-2]}")
    print(f"Created: {manifest.get('created_at', '')}")
    print()

    for spec in manifest.get("specs", []):
        spec_dir = session_dir / "specs" / spec["dir_name"]
        ev_path = spec_dir / "evidence.jsonl"
        if not ev_path.exists():
            continue

        print(f"=== {spec['name']} (ns={spec['namespace']}) ===")
        for line in open(ev_path):
            e = json.loads(line)
            event = e["event"]
            round_num = e.get("round", 0)
            role = e.get("role", "")
            data = e.get("data", {})

            if event == "round_start":
                print(f"\n  Round {round_num} ({role})")
            elif event == "agent_complete":
                cost = data.get("cost_usd", 0)
                turns = data.get("num_turns", 0)
                duration = data.get("duration_ms", 0)
                status = data.get("status", "")
                logs = data.get("logs_tail", "")

                updates = re.findall(r"<progress_update>(.*?)</progress_update>", logs)
                update = updates[-1][:150] if updates else ""

                print(
                    f"    {status}  ${cost:.2f}  {turns} turns  {duration / 1000:.0f}s"
                )
                if update:
                    print(f"    {update}")
            elif event == "game_end":
                result = data.get("game_result", "")
                total = data.get("total_cost_usd", 0)
                print(f"\n  RESULT: {result}  Total: ${total:.2f}")
            elif event in ("game_error", "error"):
                err = data.get("error", "")
                print(f"\n  ERROR: {err[:200]}")
        print()


def main():
    if len(sys.argv) < 2:
        print("Usage: evidence.py <session_id or prefix>")
        print("\nRecent sessions:")
        for d in sorted(SESSION_ROOT.iterdir(), reverse=True)[:10]:
            m = d / "session.json"
            if m.exists():
                data = json.loads(m.read_text())
                source = (
                    data.get("source_spec_path", "").split("/")[-2]
                    if "/" in data.get("source_spec_path", "")
                    else ""
                )
                print(f"  {d.name}  {source}")
        return

    analyze_session(sys.argv[1])


if __name__ == "__main__":
    main()
