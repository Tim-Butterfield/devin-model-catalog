// Package config resolves per-user locations and the small configuration file.
//
// Resolution is a pure function of the target OS, environment and home
// directory, so every platform's layout is testable on any machine.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AppName is the per-user directory name.
const AppName = "devmodels"

// DatabaseFileName is the default database file name.
const DatabaseFileName = "devmodels.db"

// Environment variables. DEVMODELS_DB overrides the configured database for a
// single invocation; DEVMODELS_CONFIG points at a different config file.
const (
	EnvDB     = "DEVMODELS_DB"
	EnvConfig = "DEVMODELS_CONFIG"
)

// Paths are the per-user locations.
type Paths struct {
	ConfigDir string `json:"config_dir"`
	DataDir   string `json:"data_dir"`
	CacheDir  string `json:"cache_dir"`
}

// ResolvePaths returns the conventional per-user locations for goos.
//
//   - macOS:   ~/Library/Application Support/devmodels (config and data),
//     ~/Library/Caches/devmodels (cache).
//   - Windows: %APPDATA%\devmodels (config), %LOCALAPPDATA%\devmodels (data;
//     local, never roaming, because a roaming profile could synchronise the
//     database mid-write), %LOCALAPPDATA%\devmodels\cache (cache).
//   - Linux and others: XDG base directories, defaulting to ~/.config,
//     ~/.local/share and ~/.cache.
func ResolvePaths(goos string, getenv func(string) string, home string) (Paths, error) {
	if home == "" {
		return Paths{}, errors.New("cannot resolve per-user directories: no home directory")
	}
	switch goos {
	case "darwin":
		support := join(goos, home, "Library", "Application Support", AppName)
		return Paths{ConfigDir: support, DataDir: support, CacheDir: join(goos, home, "Library", "Caches", AppName)}, nil
	case "windows":
		roaming := getenv("APPDATA")
		if roaming == "" {
			roaming = join(goos, home, "AppData", "Roaming")
		}
		local := getenv("LOCALAPPDATA")
		if local == "" {
			local = join(goos, home, "AppData", "Local")
		}
		return Paths{
			ConfigDir: join(goos, roaming, AppName),
			DataDir:   join(goos, local, AppName),
			CacheDir:  join(goos, local, AppName, "cache"),
		}, nil
	default:
		xdg := func(env, fallback string) string {
			if v := getenv(env); v != "" && strings.HasPrefix(v, "/") {
				return v
			}
			return join(goos, home, fallback)
		}
		return Paths{
			ConfigDir: join(goos, xdg("XDG_CONFIG_HOME", ".config"), AppName),
			DataDir:   join(goos, xdg("XDG_DATA_HOME", ".local/share"), AppName),
			CacheDir:  join(goos, xdg("XDG_CACHE_HOME", ".cache"), AppName),
		}, nil
	}
}

func join(goos string, parts ...string) string {
	if goos == "windows" {
		cleaned := make([]string, 0, len(parts))
		for i, p := range parts {
			p = strings.ReplaceAll(p, "/", `\`)
			if i > 0 {
				p = strings.Trim(p, `\`)
			} else {
				p = strings.TrimRight(p, `\`)
			}
			cleaned = append(cleaned, p)
		}
		return strings.Join(cleaned, `\`)
	}
	return filepath.ToSlash(filepath.Join(parts...))
}

// File is the persisted configuration.
type File struct {
	DBPath string `json:"db_path,omitempty"`
}

// Resolved is the effective configuration for one invocation.
type Resolved struct {
	Paths        Paths  `json:"paths"`
	ConfigFile   string `json:"config_file"`
	DBPath       string `json:"db_path"`
	DBPathSource string `json:"db_path_source"`
	BrowserDir   string `json:"browser_runtime_dir"`
}

// Load resolves the effective configuration. Precedence for the database path:
// the --db flag, then DEVMODELS_DB, then the config file, then the default.
func Load(flagDB string, getenv func(string) string) (Resolved, error) {
	home, _ := os.UserHomeDir()
	paths, err := ResolvePaths(runtime.GOOS, getenv, home)
	if err != nil {
		return Resolved{}, err
	}
	return resolve(paths, flagDB, getenv)
}

func resolve(paths Paths, flagDB string, getenv func(string) string) (Resolved, error) {
	r := Resolved{Paths: paths, BrowserDir: filepath.Join(paths.CacheDir, "browser")}
	r.ConfigFile = getenv(EnvConfig)
	if r.ConfigFile == "" {
		r.ConfigFile = filepath.Join(paths.ConfigDir, "config.json")
	}

	file, err := ReadFile(r.ConfigFile)
	if err != nil {
		return Resolved{}, err
	}

	switch {
	case flagDB != "":
		r.DBPath, r.DBPathSource = flagDB, "flag --db"
	case getenv(EnvDB) != "":
		r.DBPath, r.DBPathSource = getenv(EnvDB), "environment "+EnvDB
	case file.DBPath != "":
		r.DBPath, r.DBPathSource = file.DBPath, "config file"
	default:
		r.DBPath, r.DBPathSource = filepath.Join(paths.DataDir, DatabaseFileName), "default"
	}
	if r.DBPath != ":memory:" {
		if abs, err := filepath.Abs(r.DBPath); err == nil {
			r.DBPath = abs
		}
	}
	return r, nil
}

// ReadFile reads a config file; a missing file is an empty configuration.
func ReadFile(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("config %s is not valid JSON: %w", path, err)
	}
	return f, nil
}

// WriteFile writes a config file, creating its directory. It writes a
// temporary file and renames it into place, so an interrupted write cannot
// leave a truncated config that every later command would fail to parse. A
// config path that is a symlink keeps its link: the file it points to is
// replaced.
func WriteFile(path string, f File) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	// CreateTemp makes the file private; a config holds no secrets and was
	// always written readable, as other tools expect.
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("writing config %s: %w", path, err)
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("writing config %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
	}
	return nil
}
