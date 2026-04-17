# gh-copilot-usage

[![CI](https://github.com/SamJUK/gh-copilot-usage/actions/workflows/ci.yaml/badge.svg)](https://github.com/SamJUK/gh-copilot-usage/actions/workflows/ci.yaml)

A GitHub CLI extension that parses your local `~/.copilot` session data and visualises output token usage over time — with optional per-model breakdown and an ASCII bar chart.

## Installation

```bash
gh extension install SamJUK/gh-copilot-usage
```

Or clone and install locally:

```bash
git clone https://github.com/SamJUK/gh-copilot-usage
cd gh-copilot-usage
gh extension install .
```

## Usage

```
gh copilot-usage [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--period day\|week\|month` | `day` | Aggregation period |
| `--days N` | `30` | Number of days to include (day/week mode) |
| `--model` | false | Split token counts by model |
| `--no-graph` | false | Skip bar chart, show table only |
| `--no-table` | false | Skip table, show bar chart only |
| `--copilot-dir PATH` | `~/.copilot` | Override the copilot data directory |
| `--version` | — | Print version and exit |

### Examples

```bash
# Last 30 days (default)
gh copilot-usage

# Last 14 days, split by model
gh copilot-usage --days 14 --model

# Weekly breakdown
gh copilot-usage --period week --model

# Monthly summary, graph only
gh copilot-usage --period month --no-table
```

## How it works

The extension reads `~/.copilot/session-state/*/events.jsonl` files produced by GitHub Copilot CLI. It extracts:

- **`assistant.message`** events → `outputTokens` count + timestamp
- **`session.model_change`** events → model name at each point in time

Token counts are attributed to the model active at the time of each message. Sessions without any `session.model_change` event (older sessions) are labelled `unknown`.

> **Note:** Only **output tokens** are available in the session event data. Input tokens are not recorded.

## Building from source

```bash
go build -o gh-copilot-usage .
```

Requires Go 1.21+.
