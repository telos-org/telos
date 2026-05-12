"""Tests for analyze-runs/frontier.py — descendant classification."""

import json
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "frontier.py"


def _write_session(
    root: Path,
    session_id: str,
    *,
    spec_name: str,
    created_at: datetime,
    parent_session_id: str | None = None,
    result: str | None = None,
    status: str = "completed",
    cost: float = 0.0,
    agent_completes: list[dict] | None = None,
) -> None:
    d = root / session_id
    spec_dir = d / "specs" / "00-spec"
    spec_dir.mkdir(parents=True)
    epoch_result = None
    if status == "completed":
        epoch_result = "completed"
    elif status == "failed":
        epoch_result = "failed"
    manifest = {
        "session_id": session_id,
        "created_at": created_at.isoformat().replace("+00:00", "Z"),
        "parent_session_id": parent_session_id,
        "spec_name": spec_name,
        "specs": [{"dir_name": "00-spec", "name": spec_name}],
        "epochs": [
            {
                "id": 1,
                "started_at": created_at.isoformat().replace("+00:00", "Z"),
                "finished_at": created_at.isoformat().replace("+00:00", "Z"),
                "result": epoch_result,
            }
        ],
    }
    (d / "session.json").write_text(json.dumps(manifest))
    events = []
    seq = 0
    for ac in agent_completes or []:
        seq += 1
        events.append(
            {
                "event": "agent_complete",
                "round": ac["round"],
                "role": ac["role"],
                "data": {"status": ac["status"], "cost_usd": cost if seq == 1 else 0.0},
                "event_seq": seq,
                "epoch_id": 1,
            }
        )
    if result is not None:
        seq += 1
        events.append(
            {
                "event": "game_end",
                "round": events[-1]["round"] if events else 0,
                "role": "system",
                "data": {"game_result": result},
                "event_seq": seq,
                "epoch_id": 1,
                "ts": created_at.isoformat().replace("+00:00", "Z"),
            }
        )
    (spec_dir / "evidence.jsonl").write_text(
        "\n".join(json.dumps(e) for e in events) + ("\n" if events else "")
    )


def _run_frontier(root: Path, parent_id: str) -> dict:
    result = subprocess.run(
        [sys.executable, str(SCRIPT), "--parent", parent_id, "--json", "--limit", "20"],
        env={"TELOS_SESSION_DIR": str(root), "PATH": "/usr/bin:/bin"},
        capture_output=True,
        text=True,
        check=True,
    )
    return json.loads(result.stdout)


def test_classifies_frontier_deepening_and_p1v1(tmp_path):
    root = tmp_path / "sessions"
    root.mkdir()
    now = datetime(2026, 4, 17, tzinfo=timezone.utc)

    # The controller
    _write_session(
        root, "sup", spec_name="adversary", created_at=now - timedelta(days=1)
    )
    # A frontier-class grandchild (8 rounds, 3 pushbacks)
    _write_session(
        root,
        "mutant-frontier",
        spec_name="compound",
        created_at=now - timedelta(hours=3),
        parent_session_id="sup",
        result="success",
        cost=3.98,
        agent_completes=[
            {"round": 1, "role": "prover", "status": "CONTINUE"},
            {"round": 2, "role": "verifier", "status": "CONTINUE"},
            {"round": 3, "role": "prover", "status": "CONTINUE"},
            {"round": 4, "role": "verifier", "status": "CONTINUE"},
            {"round": 5, "role": "prover", "status": "CONTINUE"},
            {"round": 6, "role": "verifier", "status": "CONTINUE"},
            {"round": 7, "role": "prover", "status": "CONTINUE"},
            {"round": 8, "role": "verifier", "status": "CONCEDE"},
        ],
    )
    # A p1v1 grandchild
    _write_session(
        root,
        "mutant-easy",
        spec_name="drift",
        created_at=now - timedelta(hours=2),
        parent_session_id="sup",
        result="success",
        cost=0.35,
        agent_completes=[
            {"round": 1, "role": "prover", "status": "CONTINUE"},
            {"round": 2, "role": "verifier", "status": "CONCEDE"},
        ],
    )
    # A failing grandchild
    _write_session(
        root,
        "mutant-fail",
        spec_name="broken",
        created_at=now - timedelta(hours=1),
        parent_session_id="sup",
        result="failure",
        cost=0.12,
    )

    out = _run_frontier(root, "sup")
    by_class = out["totals"]["by_class"]
    assert by_class["frontier"] == 1
    assert by_class["p1v1"] == 1
    assert by_class["failing"] == 1
    assert by_class["deepening"] == 0
    # classifications attached per child
    names = {c["name"]: c["classification"] for c in out["children"]}
    assert names["compound"] == "frontier"
    assert names["drift"] == "p1v1"
    assert names["broken"] == "failing"


def test_excludes_same_name_controller_cycles(tmp_path):
    root = tmp_path / "sessions"
    root.mkdir()
    now = datetime(2026, 4, 17, tzinfo=timezone.utc)

    _write_session(
        root, "sup", spec_name="adversary", created_at=now - timedelta(days=1)
    )
    # A controller cycle (same spec_name as the controller) — should be filtered out
    _write_session(
        root,
        "cycle1",
        spec_name="adversary",
        created_at=now - timedelta(hours=4),
        parent_session_id="sup",
        result="success",
    )
    # A real mutation child of that cycle — should be included via transitive walk
    _write_session(
        root,
        "mutant",
        spec_name="compound",
        created_at=now - timedelta(hours=3),
        parent_session_id="cycle1",
        result="success",
        agent_completes=[
            {"round": 1, "role": "prover", "status": "CONTINUE"},
            {"round": 2, "role": "verifier", "status": "CONCEDE"},
        ],
    )

    out = _run_frontier(root, "sup")
    names = {c["name"] for c in out["children"]}
    assert "compound" in names
    assert "adversary" not in names
    assert out["totals"]["count"] == 1
