## Platform: local (filesystem + processes)

For local software, the materialized result is the local artifact: repo output,
binary, files, generated assets, and the commands/processes that exercise them.
The artifact and executable behavior are authoritative; journals and source
code claims are not.

Local Telos sessions share a sessions root. Child task workspaces are isolated
repo snapshots, and completed tasks publish `workspace.tar.gz` checkpoints that
include git state. Inspect child transcripts/evidence for reasoning, and extract
workspace checkpoints when you need to compare or integrate candidate work.
