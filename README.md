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

For deeper goal-contract guidance, use the checked-in
[`telos-spec-writing`](skills/telos-spec-writing/SKILL.md) skill.

Use `telos <command> --help` for the exact command surface of the installed
release.

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
