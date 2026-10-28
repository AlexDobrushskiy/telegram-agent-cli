// Package config holds the data dir layout, config.json, env switches and file-mode helpers.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/AlexDobrushskiy/telegram-agent-cli/internal/output"
)

const (
	EnvHome     = "TGAC_HOME"
	EnvReadOnly = "TGAC_READONLY"
	EnvLockWait = "TGAC_LOCK_WAIT"

	DefaultLockWait          = 90 * time.Second
	DefaultDeviceModel       = "telegram-agent-cli"
	DefaultLangCode          = "en"
	DefaultWatcherSessionEnv = "TGAC_WATCHER_SESSION"
	DefaultGlobalPerHour     = 30
)

// Paths is the data dir layout.
type Paths struct {
	Home string
}

// DefaultHome is $TGAC_HOME or ~/.config/telegram-agent-cli.
func DefaultHome() (string, error) {
	if h := os.Getenv(EnvHome); h != "" {
		return filepath.Abs(h)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "telegram-agent-cli"), nil
}

func (p Paths) Config() string   { return filepath.Join(p.Home, "config.json") }
func (p Paths) Session() string  { return filepath.Join(p.Home, "session.json") }
func (p Paths) Pending() string  { return filepath.Join(p.Home, "pending-login.json") }
func (p Paths) Peers() string    { return filepath.Join(p.Home, "peers.json") }
func (p Paths) Lock() string     { return filepath.Join(p.Home, ".lock") }
func (p Paths) Watchers() string { return filepath.Join(p.Home, "watchers.json") }
func (p Paths) Answered() string { return filepath.Join(p.Home, "answered.jsonl") }
func (p Paths) PauseAll() string { return filepath.Join(p.Home, "PAUSE-ALL") }

// Ensure creates the data dir with mode 0700 (and tightens it if it exists).
func (p Paths) Ensure() error {
	if err := os.MkdirAll(p.Home, 0o700); err != nil {
		return err
	}
	return os.Chmod(p.Home, 0o700)
}

// ReadOnly is true unless TGAC_READONLY is one of 0|false|no|off (case-insensitive).
func ReadOnly() bool {
	return ReadOnlyFrom(os.Getenv(EnvReadOnly))
}

// ReadOnlyFrom applies the TGAC_READONLY rule to a raw value.
func ReadOnlyFrom(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// LockWait is TGAC_LOCK_WAIT seconds (default 90).
func LockWait() time.Duration {
	v := strings.TrimSpace(os.Getenv(EnvLockWait))
	if v == "" {
		return DefaultLockWait
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return DefaultLockWait
	}
	return time.Duration(f * float64(time.Second))
}

// Config is config.json.
type Config struct {
	APIID             int    `json:"api_id"`
	APIHash           string `json:"api_hash"`
	Phone             string `json:"phone"`
	DeviceModel       string `json:"device_model,omitempty"`
	SystemVersion     string `json:"system_version,omitempty"`
	AppVersion        string `json:"app_version,omitempty"`
	LangCode          string `json:"lang_code,omitempty"`
	WatcherSessionEnv string `json:"watcher_session_env,omitempty"`
	GlobalPerHour     int    `json:"global_per_hour,omitempty"`
}

// WithDefaults fills empty optional fields. appVersion is the binary's version.
func (c Config) WithDefaults(appVersion string) Config {
	if c.DeviceModel == "" {
		c.DeviceModel = DefaultDeviceModel
	}
	if c.SystemVersion == "" {
		c.SystemVersion = runtime.GOOS
	}
	if c.AppVersion == "" {
		c.AppVersion = appVersion
	}
	if c.LangCode == "" {
		c.LangCode = DefaultLangCode
	}
	if c.WatcherSessionEnv == "" {
		c.WatcherSessionEnv = DefaultWatcherSessionEnv
	}
	if c.GlobalPerHour == 0 {
		c.GlobalPerHour = DefaultGlobalPerHour
	}
	return c
}

// Validate checks the fields needed to talk to Telegram.
func (c Config) Validate() error {
	if c.APIID <= 0 {
		return errors.New("api_id missing")
	}
	if !ValidAPIHash(c.APIHash) {
		return errors.New("api_hash must be 32 hex chars")
	}
	return nil
}

// ValidAPIHash reports whether s looks like a my.telegram.org api_hash.
func ValidAPIHash(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// Exists reports whether config.json exists.
func (p Paths) ConfigExists() bool {
	_, err := os.Stat(p.Config())
	return err == nil
}

// LoadConfig reads config.json; a missing file is an auth error.
func (p Paths) LoadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(p.Config())
	if errors.Is(err, os.ErrNotExist) {
		return c, output.Auth("no config at %s: run 'telegram-agent-cli auth init' first", p.Config())
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse %s: %w", p.Config(), err)
	}
	return c, nil
}

// SaveConfig writes config.json with mode 0600.
func (p Paths) SaveConfig(c Config) error {
	b, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		return err
	}
	return p.WritePrivate(p.Config(), append(b, '\n'))
}

// WritePrivate atomically writes a 0600 file inside the data dir.
func (p Paths) WritePrivate(path string, data []byte) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	return WriteFileAtomic(path, data, 0o600)
}

// WriteFileAtomic writes via a temp file + rename so readers never see a partial file.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// FixModes tightens the data dir to 0700 and every regular file in it to 0600.
func (p Paths) FixModes() {
	_ = os.Chmod(p.Home, 0o700)
	entries, err := os.ReadDir(p.Home)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			_ = os.Chmod(filepath.Join(p.Home, e.Name()), 0o600)
		}
	}
}

// Mode returns the permission bits of path as "0700", or "" if it does not exist.
func Mode(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%04o", st.Mode().Perm())
}
