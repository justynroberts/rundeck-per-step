# rundeck-per-step

A small CLI that measures Rundeck consumption on a **per-executed-step** basis.
For every job in every project it reports executions, steps defined in the job, total step-executions, and a cost number you can plug straight into a chargeback report.

```
PROJECT     JOB                EXECUTIONS  STEPS  STEP_EXECS  COST_USD
payments    deploy/api         142         8      1136        11.36
payments    deploy/worker      88          5      440         4.40
TOTAL                                              1576        15.76
```

## Why per-step

Counting whole executions punishes teams that split work into small composable jobs and rewards anyone who crams 50 steps into one job. Counting steps levels that out: 1 cent per executed step (or whatever price you set) is what the cost model is for.

## Install

One-liner (installs the latest release to `~/.local/bin`):

```bash
curl -fsSL https://raw.githubusercontent.com/justynroberts/rundeck-per-step/main/install.sh | bash
```

System-wide install:

```bash
curl -fsSL https://raw.githubusercontent.com/justynroberts/rundeck-per-step/main/install.sh | PREFIX=/usr/local/bin bash
```

Pin to a specific release:

```bash
curl -fsSL https://raw.githubusercontent.com/justynroberts/rundeck-per-step/main/install.sh | VERSION=v0.1.0 bash
```

Or grab a binary directly from the [Releases page](https://github.com/justynroberts/rundeck-per-step/releases).

## Configure

Set credentials via env vars:

```bash
export RUNDECK_URL=https://rundeck.example.com
export RUNDECK_TOKEN=xxxxxxxxxxxx
```

Multiple environments? Use named profiles selected with `--env`:

```bash
export RUNDECK_PROD_URL=...
export RUNDECK_PROD_TOKEN=...
export RUNDECK_DEV_URL=...
export RUNDECK_DEV_TOKEN=...
```

Or copy [`run.sh.example`](./run.sh.example) to `run.sh` and fill in the blanks.

## Use

```bash
rundeck-per-step                                # all-time, every project
rundeck-per-step --since 30d                    # last 30 days
rundeck-per-step --since 1m --project payments  # last month, one project
rundeck-per-step --from 2026-06-01 --to 2026-06-30
rundeck-per-step --env prod --job deploy --since 7d --price 0.02
rundeck-per-step --help                         # full reference
```

### Date windows

| Flag                     | Effect |
|--------------------------|--------|
| _(none)_                 | all-time |
| `--since 7d`             | last 7 days (Rundeck `recentFilter` syntax: `s n h d w m y`) |
| `--from … --to …`        | absolute UTC window, inclusive on both ends |

### Filters

| Flag                  | Effect |
|-----------------------|--------|
| `--env <name>`        | pick credential profile (`RUNDECK_<NAME>_URL/TOKEN`) |
| `--project <substr>`  | case-insensitive substring match on project name |
| `--job <substr>`      | case-insensitive substring match on job name or `group/name` |
| `--price <usd>`       | price per executed step. Default `0.01` |
| `--api <version>`     | Rundeck API version. Default `46` |
| `--quiet`             | disable the live spinner |

Jobs with zero executions in the selected window are hidden from the table.

## How counting works

- **Steps**: static count of top-level workflow steps in the job definition (`sequence.commands`). Error handlers and nested-workflow sub-steps are not expanded.
- **Executions**: total in the selected window, via `/api/V/project/{name}/executions?jobIdListFilter=...`. (Note: Rundeck's `/job/{id}/executions` endpoint silently ignores `recentFilter`; we don't use it.)
- **Cost**: `price × steps × executions`. A 20-step job that ran once is 20 step-executions.

## Build from source

```bash
git clone https://github.com/justynroberts/rundeck-per-step
cd rundeck-per-step
go build -o rundeck-per-step .
./build.sh   # cross-compile for darwin/linux/windows × amd64/arm64
```

## License

MIT
