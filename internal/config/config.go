// Package config reads the repository's .axeqrc.json, which tells axeq how to
// bring up the application under test and what to audit. The file lives in
// the root of the repository being audited — the CWD when axeq runs inside
// the workflow.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the config file's name, looked up in the CWD.
const FileName = ".axeqrc.json"

// Config is the whole file: everything axeq needs to know about the repository.
type Config struct {
	Version int `json:"version"`
	// App describes how to start the application under test. Optional: a
	// repository whose URLs are already reachable (a deployed preview, say)
	// has none.
	App *App `json:"app,omitempty"`
	// Urls are the entry points to audit; each must be an absolute URL. At
	// least one is required, and the first is the default start page.
	Urls []string `json:"urls"`
	// Schedule holds the defaults for the schedule command.
	Schedule *Schedule `json:"schedule,omitempty"`
}

// App describes how to bring up the application under test. Commands run via
// `sh -c` from Cwd (default: the repository root).
type App struct {
	Cwd string `json:"cwd,omitempty"`
	// Setup commands run to completion, in order, before Start (e.g. npm ci).
	Setup []string `json:"setup,omitempty"`
	// Start is the long-running server command. It is killed once the audit
	// is done.
	Start string `json:"start"`
	// Env is added to the environment of every setup and start command.
	Env map[string]string `json:"env,omitempty"`
	// ReadyUrl is polled with GET until it answers with a status below 500;
	// the audit starts only then. Default: the first entry of Urls.
	ReadyUrl string `json:"readyUrl,omitempty"`
	// ReadyTimeoutSeconds bounds that wait. Default 60.
	ReadyTimeoutSeconds int `json:"readyTimeoutSeconds,omitempty"`
}

// Schedule holds the schedule command's defaults. Zero values mean "use the
// command's own default"; command-line flags override these.
type Schedule struct {
	Prompt           string `json:"prompt,omitempty"`
	MaxScenarios     int    `json:"maxScenarios,omitempty"`
	MaxExploreTurns  int    `json:"maxExploreTurns,omitempty"`
	MaxScenarioTurns int    `json:"maxScenarioTurns,omitempty"`
}

// DefaultReadyTimeoutSeconds applies when App.ReadyTimeoutSeconds is unset.
const DefaultReadyTimeoutSeconds = 60

// ErrNotFound is returned by Load when the directory holds no config file.
var ErrNotFound = errors.New("config file not found")

// Load reads and validates FileName from dir. It returns ErrNotFound (wrapped)
// when the file is absent so callers can tell that apart from a broken file.
func Load(dir string) (Config, error) {
	path := filepath.Join(dir, FileName)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks a config is usable: a known version, at least one absolute
// URL, and, when an app is declared, a start command and an absolute ready URL.
func Validate(cfg Config) error {
	if cfg.Version != 1 {
		return fmt.Errorf("unsupported version %d (expected 1)", cfg.Version)
	}
	if len(cfg.Urls) == 0 {
		return errors.New("\"urls\" needs at least one entry")
	}
	for _, raw := range cfg.Urls {
		if !absoluteUrl(raw) {
			return fmt.Errorf("\"urls\" entry %q is not an absolute URL", raw)
		}
	}
	if cfg.App != nil {
		if strings.TrimSpace(cfg.App.Start) == "" {
			return errors.New("\"app.start\" is required when \"app\" is set")
		}
		if cfg.App.ReadyUrl != "" && !absoluteUrl(cfg.App.ReadyUrl) {
			return fmt.Errorf("\"app.readyUrl\" %q is not an absolute URL", cfg.App.ReadyUrl)
		}
		if cfg.App.ReadyTimeoutSeconds < 0 {
			return errors.New("\"app.readyTimeoutSeconds\" must not be negative")
		}
	}
	return nil
}

// ReadyUrl is the URL to poll for app readiness: the explicit one, else the
// first entry of Urls.
func ReadyUrl(cfg Config) string {
	if cfg.App != nil && cfg.App.ReadyUrl != "" {
		return cfg.App.ReadyUrl
	}
	return cfg.Urls[0]
}

// ReadyTimeoutSeconds is the readiness wait bound, defaulted.
func ReadyTimeoutSeconds(app App) int {
	if app.ReadyTimeoutSeconds > 0 {
		return app.ReadyTimeoutSeconds
	}
	return DefaultReadyTimeoutSeconds
}

func absoluteUrl(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme != "" && u.Host != ""
}
