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

## Compare exact local builds

To compare a baseline and a code change, build `telos` and `telosd` from each
revision into separate directories. Build for the task container's OS and CPU,
not your host: these commands target Linux amd64, including when run on macOS.
Use `GOARCH=arm64` instead if your task containers use arm64.

Run this in the clean baseline checkout, using its commit as the version label:

```bash
mkdir -p /tmp/telos-baseline
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-X main.Version=baseline-$(git rev-parse --short HEAD)" \
  -o /tmp/telos-baseline/ ./cmd/telos ./cmd/telosd
```

Run the same build in the changed checkout with `candidate` in the label and
`/tmp/telos-candidate/` as the output directory. Save the baseline commit and
the candidate commit or patch alongside the results. Then use the updated
Harbor shim for both runs, selecting only the binary directory differently:

```bash
TELOS_HARBOR_TELOS_BINARY_DIR=/tmp/telos-baseline \
TELOS_HARBOR_JOB_NAME=telos-baseline \
./integrations/harbor/run_scbench_circuit_eval.sh

TELOS_HARBOR_TELOS_BINARY_DIR=/tmp/telos-candidate \
TELOS_HARBOR_JOB_NAME=telos-candidate \
./integrations/harbor/run_scbench_circuit_eval.sh
```

These commands run the model and incur normal benchmark costs. The directory
is on the machine running Harbor; the shim uploads both binaries into each
task container, replacing any existing Telos installation and selecting the
uploaded daemon even if the container has a different `TELOSD_PATH`. Missing or
unexecutable binaries fail installation instead of falling back to a release.
Direct Harbor invocations can use `--ak telos_binary_dir=/absolute/build/path`.
This option requires `install_telos=true`, the default. Each trial records
the installed versions and SHA-256 hashes in `telos-harbor-build.log`.

Keep the task set, model, thinking level, turn and cost limits, skills, Pi
version, Harbor version, and container images the same in both runs. Save
those versions/settings with your results; selecting a binary directory pins
Telos only. Compare pass rates across repeated attempts, alongside cost and
runtime. Log-reader microbenchmarks measure efficiency, not task quality.
