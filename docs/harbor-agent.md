# Telos As A Harbor Agent

Harbor owns the benchmark lifecycle and container. Telos is installed into that
container as an executable agent.

The Harbor shim lives at:

```bash
integrations.harbor.telos_agent:TelosExecutableAgent
```

It does four things:

1. Installs `telos` and `telosd` from the release artifact path.
2. Installs Pi when missing.
3. Renders Harbor's instruction into a local Telos `SPEC.md`.
4. Runs `telos run` against Harbor's task workspace and waits for the Telos
   session to finish.

Example:

```bash
uvx harbor run \
  -d gabeorlanski/slopcodebench \
  -i '*circuit*eval*' \
  --agent-import-path integrations.harbor.telos_agent:TelosExecutableAgent \
  --model openai-codex/gpt-5.5 \
  --ak thinking=high \
  --ak until=3 \
  --ak session_timeout_sec=7200 \
  --ak pi_config_source=/tmp/host-pi-agent \
  --ak inject_pi_models=false \
  --mounts '[{"type":"bind","source":"/Users/rohangupta/.pi/agent","target":"/tmp/host-pi-agent","read_only":true,"bind":{"create_host_path":false}}]' \
  --jobs-dir eval-runs/harbor
```

The shim sets `TELOS_SESSION_DIR=/tmp/telos-harbor/sessions` before invoking
`telos run`, so Telos evidence and transcripts do not pollute the scored
workspace.
