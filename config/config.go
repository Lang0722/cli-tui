// Package config loads and saves agenttui's user settings, a TOML file at
// $XDG_CONFIG_HOME/agenttui/config.toml (~/.config/agenttui/config.toml).
// The layout mirrors the [tui] table of Codex' config.toml.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"agenttui/backend/mcp"
)

// Config is the whole file.
type Config struct {
	TUI TUI `toml:"tui"`
	// McpServers are the MCP servers ([mcp_servers.<name>], Codex's
	// layout) for the built-in mock backend; see backend/mcp.ServerConfig.
	McpServers map[string]mcp.ServerConfig `toml:"mcp_servers,omitempty"`
}

// TUI holds the UI settings.
type TUI struct {
	// StatusLine lists the footer status-line items in order. nil means the
	// default; an empty list hides the status line.
	StatusLine *[]string `toml:"status_line,omitempty"`
	// StatusLineUseColors colors each status-line item by kind (default on).
	StatusLineUseColors *bool `toml:"status_line_use_colors,omitempty"`
	// TerminalTitle lists the window-title items in order (nil: default).
	TerminalTitle *[]string `toml:"terminal_title,omitempty"`
	// Keymap maps context → action → key bindings (see tui/keymap.go).
	Keymap map[string]map[string]Bindings `toml:"keymap,omitempty"`
	// AgentsView is how the agent command center was last shown: its
	// status tab and its grouping. Saved when the center closes.
	AgentsView *AgentsView `toml:"agents_view,omitempty"`
}

// AgentsView is the command center's view: Filter is "all", "needs_you",
// "working", "ready" or "inactive"; Group is "project", "directory",
// "status" or "model". Unknown values fall back to the defaults.
type AgentsView struct {
	Filter string `toml:"filter,omitempty"`
	Group  string `toml:"group,omitempty"`
}

// Bindings is one action's keys. In TOML it is either a string or an
// array of strings; an empty array unbinds the action.
type Bindings []string

func (b *Bindings) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case string:
		*b = Bindings{v}
	case []any:
		out := Bindings{}
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return fmt.Errorf("key binding must be a string, got %T", e)
			}
			out = append(out, s)
		}
		*b = out
	default:
		return fmt.Errorf("key binding must be a string or an array of strings, got %T", v)
	}
	return nil
}

// Path is the config file location.
func Path() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "agenttui", "config.toml")
}

// Load reads the file at path. A missing file is an empty config.
func Load(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return c, fmt.Errorf("%s: unknown setting %q", path, und[0].String())
	}
	for name, s := range c.McpServers {
		if err := s.Validate(); err != nil {
			return c, fmt.Errorf("%s: [mcp_servers.%s]: %w", path, name, err)
		}
	}
	return c, nil
}

const header = "# agenttui settings. /statusline, /title and /keymap rewrite this file.\n\n"

// Save writes c to path atomically.
func Save(path string, c Config) error {
	var buf bytes.Buffer
	buf.WriteString(header)
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// MarshalTOML writes one binding as a string and several (or none, which
// unbinds) as an array, like Codex' keymap editor.
func (b Bindings) MarshalTOML() ([]byte, error) {
	q := func(s string) string { return strconv.Quote(s) }
	if len(b) == 1 {
		return []byte(q(b[0])), nil
	}
	parts := make([]string, len(b))
	for i, s := range b {
		parts[i] = q(s)
	}
	return []byte("[" + strings.Join(parts, ", ") + "]"), nil
}
