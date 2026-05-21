# Harbor Integration

This directory makes Telos available to Harbor as an executable agent. Harbor
loads the Python shim, but the evaluated agent is the Go `telos` binary.

The shim:

- installs Telos inside the task container from
  `https://usetelos.ai/releases/latest/install.sh`
- renders the Harbor task as a local Telos `SPEC.md`
- runs `telos run ... --until ...` in the benchmark workspace
- writes the generated SPEC and Telos stdout/stderr into the Harbor trial logs

## Reproduce SCBench Circuit Eval

From the Telos repo:

```bash
cd /path/to/telos

OPENAI_API_KEY=... \
TELOS_HARBOR_MODEL=openai-codex/gpt-5.5 \
TELOS_HARBOR_UNTIL=1 \
TELOS_HARBOR_SKILLS='verify-engineering*' \
./integrations/harbor/run_scbench_circuit_eval.sh
```

That reproduces the baseline shape recorded in `SCBENCH_REPORT_05_21.md`:
SCBench `circuit_eval`, Telos installed through the public release installer,
and one implementation/evaluation cycle per checkpoint.

For the quality-regression run, use repair turns and include the quality rubric:

```bash
TELOS_HARBOR_UNTIL=3 \
TELOS_HARBOR_SKILLS='verify-engineering*,verify-quality*' \
./integrations/harbor/run_scbench_circuit_eval.sh
```

If the model is configured through Pi rather than plain environment variables,
mount the host Pi config read-only:

```bash
TELOS_HARBOR_PI_CONFIG_SOURCE="$HOME/.pi/agent" \
TELOS_HARBOR_INJECT_PI_MODELS=false \
./integrations/harbor/run_scbench_circuit_eval.sh
```

Results are written under:

```text
/tmp/telos-harbor-jobs/<job-name>/result.json
```

Per-trial logs include:

- `telos-harbor-spec.md`
- `telos-harbor-stdout.log`
- `telos-harbor-stderr.log`

