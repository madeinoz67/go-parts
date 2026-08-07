# Configuration

go-parts' non-secret run-time configuration lives in
`<data_dir>/config.json` and overlays bare-metal defaults. The data directory
defaults to `~/.go-parts` (mirrors go-rag / MuninnDB).

## Keys

| Key | Default | Notes |
|---|---|---|
| `data_dir` | `~/.go-parts` | location of the Pebble store, `daemon.json`, and `config.json` itself |
| `bind` | `127.0.0.1:7890` | HTTP listen address; loopback-only by default (§5.18) |

Example:

```json
{
  "data_dir": "/home/maker/.go-parts",
  "bind": "127.0.0.1:7890"
}
```

## Load semantics

`config.Load` is "default everything, then overlay saved values":

- **Missing file** — not an error; defaults stand (first run).
- **Read error other than "not exist"** — returned (the filesystem is sick;
  the operator should know).
- **Malformed JSON** — **warned** (`slog.Warn`) and defaults stand. The file
  is non-secret advice, not a load-bearing contract; refusing to start because
  `config.json` had a stray comma would be worse than falling back to defaults
  with a working binary. (CLAUDE.md §2: degrade loudly-but-gracefully, never
  silently-wrong.)
- **Empty fields in the file** — defaults are kept (JSON "absent key"
  semantics); only non-empty saved values overlay.

`--data-dir` (CLI) and `--bind` (`start` only) override the file. See the
[CLI reference](../guide/cli.md).

## Secrets posture (§5.18)

**No secret is ever written to `config.json`.** The config package holds only
non-secret settings — the data directory and the bind address. Authentication
of every kind lives in **environment variables** and is never read, written,
or marshalled by the config layer:

- `GOPARTS_*` — vendor API tokens, future go-parts-native secrets.
- `GORAG_TOKEN` — go-rag gateway token (when the §5.5 search gateway is on).
- `MUNINNDB_TOKEN` — MuninnDB gateway token (when the §5.5 memory gateway is on).

**None of these exist in the v1 slice** — there is no vendor plugin, gateway,
or auth surface yet — but the invariant holds from day one: after a restore,
no secret comes back from `config.json`; secrets must be re-supplied via env.
That is the deliberate trade for never shipping a secret in a tracked or
backed-up file.

## Default bind — loopback

`DefaultBind = "127.0.0.1:7890"`. go-parts never exposes a writable surface
to the network without an explicit operator action (`--bind` on `start`, or a
non-loopback `bind` in `config.json`). v1 is single-operator; a non-loopback
bind is the operator opting into network exposure.
