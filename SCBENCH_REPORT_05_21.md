# SCBench Report - 2026-05-21

This report tracks Telos runs against SCBench/SlopCodeBench using the Go
runtime as a Harbor executable agent.

## Baseline A: Telos `--until 1`

- Dataset: `gabeorlanski/slopcodebench`
- Task: `gabeorlanski/circuit_eval`
- Trial: `circuit_eval__VVdcxSM`
- Agent: `integrations.harbor.telos_agent:TelosExecutableAgent`
- Model: `openai-codex/gpt-5.5`
- Thinking: `high`
- Telos runtime installed in container: `telos v0.0.0-dev.b3dbdee9215d`
- Review policy: `--until 1`
- Required Telos rubric: `verify-engineering*`
- Job: `/tmp/telos-harbor-jobs/telos-scbench-circuit-smoke-20260521-085157`
- Result file: `/tmp/telos-harbor-jobs/telos-scbench-circuit-smoke-20260521-085157/result.json`
- Runtime: `1h 21m 55s`
- Cost recorded by Harbor: `$0.830842`

### Reproduction

The reproducible Harbor entry point now lives at:

```bash
./integrations/harbor/run_scbench_circuit_eval.sh
```

Baseline-equivalent invocation:

```bash
TELOS_HARBOR_MODEL=openai-codex/gpt-5.5 \
TELOS_HARBOR_UNTIL=1 \
TELOS_HARBOR_SKILLS='verify-engineering*' \
./integrations/harbor/run_scbench_circuit_eval.sh
```

The Harbor shim installs Telos inside the task container via:

```bash
TELOS_INSTALL_DIR="$HOME/.local/bin" \
curl -fsSL https://usetelos.ai/releases/latest/install.sh | sh
```

### Aggregate Metrics

| Metric | Value |
| --- | ---: |
| Core pass rate mean | 0.985294 |
| Strict pass rate mean | 0.998332 |
| Isolated pass rate mean | 0.985931 |
| Verbosity mean | 0.406401 |
| Erosion mean | 0.679543 |
| Verbosity increase rate | 0.571429 |
| Erosion increase rate | 0.571429 |
| Missing trial count | 0 |

### Checkpoint Metrics

| Checkpoint | Telos session | Core | Strict | Isolated | Verbosity | Verbosity increased | Erosion | Erosion increased |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | `local_20260521_125229_00` | 1.000000 | 1.000000 | 1.000000 | 0.281481 | 0 | 0.278282 | 0 |
| 2 | `local_20260521_130325_00` | 1.000000 | 1.000000 | 1.000000 | 0.332090 | 1 | 0.582685 | 1 |
| 3 | `local_20260521_130820_00` | 1.000000 | 1.000000 | 1.000000 | 0.455069 | 1 | 0.787024 | 1 |
| 4 | `local_20260521_131800_00` | 1.000000 | 0.997674 | 0.995556 | 0.487042 | 1 | 0.816895 | 1 |
| 5 | `local_20260521_132732_00` | 1.000000 | 0.997812 | 1.000000 | 0.523009 | 1 | 0.816444 | 0 |
| 6 | `local_20260521_133602_00` | 1.000000 | 1.000000 | 1.000000 | 0.455180 | 0 | 0.740182 | 0 |
| 7 | `local_20260521_134935_00` | 1.000000 | 1.000000 | 1.000000 | 0.367641 | 0 | 0.707103 | 0 |
| 8 | `local_20260521_140023_00` | 0.882353 | 0.991166 | 0.891892 | 0.349691 | 0 | 0.707732 | 1 |

### Readout

Correctness was strong. Telos reached a `0.985294` core pass rate across the
full eight-checkpoint `circuit_eval` task, with checkpoints 1-7 at `1.0` core
pass rate and checkpoint 8 pulling the mean down.

The run did not validate the slop-correction thesis. It used `--until 1`, so
each checkpoint received one implementation turn and one evaluation turn. The
evaluator could observe quality issues, but there was no repair turn after its
findings. The rendered SPEC required `verify-engineering*`; `verify-quality`
was available as a built-in skill but was not a required rubric.

SCBench still measured meaningful code erosion:

- verbosity mean: `0.406401`
- erosion mean: `0.679543`
- verbosity increased on 4 of 7 continuation transitions
- erosion increased on 4 of 7 continuation transitions

### Harness Notes

The saved Harbor result for this baseline contains per-step `exception_info`
because the initial Harbor shim treated Telos process exit `1` as fatal even
when marked Telos session JSON reported `status: completed`. This has been
fixed in commit `339ba37` (`fix(integrations): accept completed Telos Harbor
sessions`) after the baseline run. The benchmark rewards above are still
present and were written by Harbor.

## Planned Run B: Telos `--until 3` With Quality Rubric

Planned changes before launch:

- Run with `--until 3` so evaluator findings feed into repair turns.
- Require both `verify-engineering*` and `verify-quality*` in the rendered
  Harbor SPEC.
- Update `verify-quality` so the evaluator judges the resulting system and
  surrounding codebase in context, while keeping the builder-visible SPEC
  focused on the task contract.

Target comparison:

- Preserve or improve core/strict pass rates.
- Reduce or stabilize verbosity and erosion relative to Baseline A.
- Inspect whether evaluator findings about slop become concrete repair work in
  the next implementation turns.

Run B results will be appended here after completion.
