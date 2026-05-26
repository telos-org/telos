## Platform: cloud (Kubernetes)

For cloud software, the materialized result is the Kubernetes environment:
resources, routes, storage, and service behavior. Live cluster behavior is
authoritative; manifests and source code claims are not.

Cloud Telos sessions share environment-local session state. Child tasks get
isolated workers, namespace lineage, transcripts/evidence, and workspace
checkpoints. The runtime injects the session identity and credentials needed
for the Telos CLI surface inside controller/task workers. Inspect session
lineage first; use Kubernetes probes when live resource behavior is the
load-bearing question.
