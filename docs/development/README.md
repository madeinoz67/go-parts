# Developing go-parts

How we work here. For what we're building, see the [PRD](../internals/go-parts-prd.md); for the
non-negotiables, see [CLAUDE.md](../../CLAUDE.md).

## Setup

First time here? See [setup.md](setup.md) for the Claude Code agent/hooks config and the
MuninnDB memory connection (the go-parts vault key) — both are needed for the review + memory
substrate to work.

## Build & test

go-parts uses Go 1.26+ and the standard `cmd/` + `internal/` layout.

```bash
make build      # → bin/go-parts
make test       # race detector + coverage
make vet        # go vet
make lint       # golangci-lint (v2)
```

CI (`.github/workflows/ci.yml`) runs lint, govulncheck, `go test -race`, build.

## Branching

`develop` is the integration trunk; `main` is pristine (release merges only). All work lands
on `develop`. See [CLAUDE.md §3](../../CLAUDE.md).

## How we work (the rest)

- [Review process](review-process.md) — the `code-reviewer` + `adversary` agents, the `panel`
  skill, and the documentation gate.
- [Memory substrate](memory-substrate.md) — how durable findings reach the go-parts vault.
- [Doc obligations](doc-obligations.md) — what a PR must keep current.
