## Target: cloud

For cloud software, the materialized result is the live managed environment:
services, storage, routes, and observable behavior. Live behavior is
authoritative; manifests and source code claims are not.

Cloud Telos sessions share environment-local session state. Child tasks get
isolated workers, session lineage, transcripts/evidence, and workspace
checkpoints. The runtime injects the session identity and credentials needed for
the Telos CLI surface inside controller/task workers. Inspect session lineage
first; use live service probes when runtime behavior is the load-bearing
question.
