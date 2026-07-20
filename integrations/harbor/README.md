# Harbor Integration

[Harbor](https://pypi.org/project/harbor/) is a harness for running coding
agents against containerized benchmark tasks; we use it to evaluate Telos on
SCBench. This directory makes Telos available to Harbor as an executable
agent: Harbor loads the Python shim, but the evaluated agent is the Go
`telos` binary.

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
TELOS_HARBOR_SKILLS='@telos/verify-engineering:0.1.0*' \
./integrations/harbor/run_scbench_circuit_eval.sh
```

That runs SCBench `circuit_eval` with Telos installed through the public
release installer and one implementation/evaluation cycle per checkpoint.

For the quality-regression run, use repair turns and include the quality rubric:

```bash
TELOS_HARBOR_UNTIL=3 \
TELOS_HARBOR_SKILLS='@telos/verify-engineering:0.1.0*,@telos/verify-quality:0.1.0*' \
./integrations/harbor/run_scbench_circuit_eval.sh
```

To run repeated attempts for the same task, use Harbor attempts:

```bash
TELOS_HARBOR_UNTIL=5 \
TELOS_HARBOR_N_ATTEMPTS=3 \
TELOS_HARBOR_N_CONCURRENT=1 \
TELOS_HARBOR_SKILLS='@telos/verify-engineering:0.1.0*,@telos/verify-quality:0.1.0*' \
./integrations/harbor/run_scbench_circuit_eval.sh
```

To run in Modal sandboxes instead of local Docker:

```bash
TELOS_HARBOR_ENV=modal \
TELOS_HARBOR_PI_CONFIG_SOURCE="$HOME/.pi/agent" \
TELOS_HARBOR_INJECT_PI_MODELS=false \
./integrations/harbor/run_scbench_circuit_eval.sh
```

Modal uses Harbor's Docker-in-Docker strategy by default so benchmark images
stay alive and executable inside the sandbox. Set `TELOS_HARBOR_MODAL_DIND=false`
to use Harbor's direct Modal strategy instead. Non-Docker environments use
`uvx 'harbor[<env>]'` by default so provider extras such as `harbor[modal]` are
installed. Set `TELOS_HARBOR_USE_LOCAL=true` to use the local `harbor`
executable instead.

If the model is configured through Pi rather than plain environment variables,
mount the host Pi config read-only:

```bash
TELOS_HARBOR_PI_CONFIG_SOURCE="$HOME/.pi/agent" \
TELOS_HARBOR_INJECT_PI_MODELS=false \
./integrations/harbor/run_scbench_circuit_eval.sh
```

Results are written under:

```text
eval-runs/harbor/<job-name>/result.json
```

Per-trial logs include:

- `telos-harbor-spec.md`
- `telos-harbor-stdout.log`
- `telos-harbor-stderr.log`
