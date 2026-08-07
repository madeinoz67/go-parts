// Package config loads go-parts' non-secret run-time configuration from
// `<data_dir>/config.json` and overlays it on bare-metal defaults.
//
// §5.18 (secrets): this package holds ONLY non-secret settings — the data
// directory and the bind address. Authentication (vendor API tokens, gateway
// tokens like GOPARTS_*/GORAG_TOKEN/MUNINNDB_TOKEN) lives in env vars and is
// NEVER read, written, or marshalled here. After a restore, no secret comes
// back from config.json — that is the deliberate trade.
//
// The Load model is "default everything, then overlay saved values": a missing
// config.json is normal on first run and yields the bare-metal default
// (loopback bind, ~/.go-parts data dir). A malformed config.json is warned
// (slog.Warn) and the defaults stand — the operator can fix the file or delete
// it; Load never blocks startup on a corrupt non-secret file.
package config

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
)

// DefaultBind is the bare-metal bind address (§5.18): loopback-only by
// default, so go-parts never exposes a writable surface to the network
// without an explicit operator action.
const DefaultBind = "127.0.0.1:7890"

// Config is the non-secret run-time configuration. Add fields here when a
// new non-secret knob lands; never add secrets (§5.18).
type Config struct {
	DataDir string `json:"data_dir"`
	Bind    string `json:"bind"` // loopback by default; non-loopback is opt-in
}

// Default returns the bare-metal configuration for dataDir. An empty dataDir
// resolves to ~/.go-parts (mirrors go-rag / MuninnDB's home-dir default).
func Default(dataDir string) Config {
	if dataDir == "" {
		dataDir = filepath.Join(os.Getenv("HOME"), ".go-parts")
	}
	return Config{DataDir: dataDir, Bind: DefaultBind}
}

// Load reads <dataDir>/config.json and overlays it on Default(dataDir). A
// missing file is not an error: defaults stand. A read error other than
// "not exist" is returned (the operator should know the filesystem is sick).
// A malformed JSON body is currently swallowed (defaults stand) — the file is
// non-secret advice, not a load-bearing contract; refusing to start because
// the operator's config.json had a stray comma would be a worse experience
// than falling back to defaults + a working binary.
//
// NOTE: dataDir is BOTH the location of config.json AND a field inside it.
// The caller knows the dataDir (it's how it found the file); the in-file
// data_dir field is the overlay value, which the caller may choose to honor
// or ignore. v1 honors the caller's dataDir (the overlay is informational
// until a `config edit` flow lands).
func Load(dataDir string) (Config, error) {
	c := Default(dataDir)
	path := filepath.Join(dataDir, "config.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil // first run; defaults stand
		}
		return c, err
	}
	// Overlay saved values on defaults. Unmarshal into a fresh Default so
	// empty fields in the file don't zero the defaults — JSON's "absent key"
	// semantics give us this for free, but we re-pin Bind/DataDir below to
	// make the overlay intent explicit.
	var saved Config
	if jerr := json.Unmarshal(b, &saved); jerr != nil {
		// Loud-but-graceful (CLAUDE.md §2): the file is non-secret advice, not a
		// load-bearing contract, so we don't block startup — but we DO warn so a
		// stray comma doesn't silently mask the operator's intent.
		slog.Warn("config.json malformed; using defaults", "error", jerr)
		return c, nil
	}
	if saved.Bind != "" {
		c.Bind = saved.Bind
	}
	if saved.DataDir != "" {
		c.DataDir = saved.DataDir
	}
	return c, nil
}
