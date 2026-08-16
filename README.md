# Telos

Telos is a goal-oriented agent runtime. It turns declarative software outcomes
into bounded agent runs or durable reconciled services.

## Install

```bash
curl -fsSL https://usetelos.ai/install.sh | sh
telos --version
```

The checksummed installer supports macOS and Linux on amd64 and arm64. It
installs `telos`, `telosd`, and the open
[`@telos/telos-cli`](skills/telos-cli/SKILL.md) agent skill. That skill is the
canonical usage documentation for humans working through interactive coding
agents.

Give your coding agent an outcome, or start with the canonical
[quickstart prompt](skills/telos-cli/assets/quickstart-prompt.txt). The agent
will help write the smallest verifiable `SPEC.md`, plan it, and choose a bounded
`telos run` or durable `telos apply` workflow.

Use `telos <command> --help` for the exact command surface of the installed
release.

## Managed Cloud snapshot safety

A claimed Cloud runtime (`TELOS_SESSION_ID=sess_...`) advertises
`checkpoint_safe_point` in the live `/api/healthz` `capabilities` list. The
Cloud host can then call the authenticated, operator-only
`POST /internal/checkpoint/prepare` and `POST /internal/checkpoint/resume`
endpoints for that exact session claim.

Prepare durably closes admission for session changes, bootstrap and worker
supervision, and worker cycles before waiting for admitted work to finish.
Every prepare and resume includes the same `operation_id`, so a late
resume from an older snapshot cannot reopen work paused for a newer snapshot.
A timeout or process restart leaves admission closed for that operation until
its explicit, idempotent resume. Existing deployments gain this capability
only when deliberately pinned to a runtime release that contains it.

Checkpoint state uses durable files and fails closed if initialized state is
missing, malformed, or replaced by a symbolic link. The runtime and other
tools running as the same Unix user are still one trusted security boundary:
these checks protect against corruption and accidental path replacement, not
against a malicious program already running as that same user.

## Develop

```bash
go test ./...
go build ./cmd/telos ./cmd/telosd
bazel test //...
```

Release builds use `scripts/publish-release.sh`. Protected `master` merges
publish immutable, commit-addressed binaries and the canonical skill bundle,
verify them, and promote `latest` only after the complete release exists.

## License

Fair Source (FSL-1.1), converting to Apache-2.0 two years after each release.
