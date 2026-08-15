# Troubleshooting

Start with facts:

```bash
telos --version
telos config
telos plan SPEC.md
telos describe SESSION_ID
telos logs --verbose SESSION_ID
```

Use `--json` when exact fields or automated diagnosis matter. Do not paste
credential files, bearer tokens, or unrestricted environment dumps into logs
or support messages.

## Command not found

Verify the installer completed and `${TELOS_INSTALL_DIR:-$HOME/.local/bin}` is
on `PATH`. Re-run the checksummed installer when the binary is absent or the
installed release is not the intended version.

## Local run cannot start

Confirm the spec says `platform: local`, `pi` is on `PATH`, and the selected pi
provider is authenticated. Use `telos run --help` to confirm model, thinking,
cycle, time, and cost flags supported by the installed release.

## Cloud command is unauthenticated or targets the wrong team

Run `telos config`. Log in again if authentication is invalid. Set the intended
context explicitly; do not mutate a team deployment while the target is
ambiguous.

## Plan or publish rejects a spec or skill

Read the first validation error. Check YAML frontmatter, semantic version,
non-empty instructions, safe file paths, and exact registry refs. If a version
already exists with different content, bump the version rather than attempting
to overwrite it.

## Deployment is not ready

Inspect `describe` and `logs` before retrying. Distinguish agent work, verifier
rejection, package-digest mismatch, runtime failure, and public-service failure.
Do not create a second deployment merely to hide an actionable state in the
first one.

## Nested run is rejected

Nested execution supports `telos run`, not `telos apply`. Confirm the parent is
running under Telos, the child spec is reachable inside its workspace, and the
requested bounds are valid.
