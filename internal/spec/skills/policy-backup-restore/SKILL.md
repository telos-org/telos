---
name: policy-backup-restore
description: |
  Backup and restore overlay. Use when the spec requires recoverability,
  snapshotting, or proof that data can be restored after failure. A backup is
  only real if restore is exercised. Triggers on: backup, restore, recovery,
  snapshot, disaster recovery.
metadata:
  category: policy
  author: telos
allowed-tools: Bash(kubectl:*) Bash(pg_dump:*) Bash(pg_restore:*) Bash(psql:*) Bash(bao:*)
---

# Policy Overlay: Backup and Restore

Goal: produce a real backup artifact and prove it can restore known data.

## Default Approach

A valid implementation has four steps:

1. Seed a sentinel record
2. Create a backup artifact
3. Simulate loss or drift
4. Restore and compare

If step 4 is missing, the policy is not satisfied.

## Artifact Rules

Backups should be written to a named artifact, not just mentioned in docs.

Examples:

- `/workspace/output/backups/postgres.dump`
- `/workspace/output/backups/openbao.snap`

The artifact path should be easy for the verifier to inspect.

## Product Patterns

- PostgreSQL: `pg_dump` + `pg_restore` or logical export/import
- OpenBao: raft snapshot save/restore
- ClickHouse: native backup/restore or deterministic export/import

Prefer the simplest real path that the product natively supports.

## Verification Pattern

Write one restore drill that:

- creates known data
- backs it up
- deletes or mutates the live data
- restores from the artifact
- verifies the original data is back

## Good Outputs

Produce:

- the backup command or manifest
- the restore command or manifest
- one executable recovery test using sentinel data

## Anti-Patterns

- treating replication as backup
- calling a copied file a backup without restore proof
- taking a snapshot after the restore point has already been lost
- writing a test that only checks "backup command exited 0"
