package config

import (
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestResolvePathsPerPlatform(t *testing.T) {
	cases := []struct {
		goos string
		env  map[string]string
		home string
		want Paths
	}{
		{"darwin", nil, "/Users/u", Paths{
			ConfigDir: "/Users/u/Library/Application Support/devmodels",
			DataDir:   "/Users/u/Library/Application Support/devmodels",
			CacheDir:  "/Users/u/Library/Caches/devmodels",
		}},
		{"windows", map[string]string{"APPDATA": `C:\Users\u\AppData\Roaming`, "LOCALAPPDATA": `C:\Users\u\AppData\Local`}, `C:\Users\u`, Paths{
			ConfigDir: `C:\Users\u\AppData\Roaming\devmodels`,
			DataDir:   `C:\Users\u\AppData\Local\devmodels`,
			CacheDir:  `C:\Users\u\AppData\Local\devmodels\cache`,
		}},
		{"windows", nil, `C:\Users\u`, Paths{
			ConfigDir: `C:\Users\u\AppData\Roaming\devmodels`,
			DataDir:   `C:\Users\u\AppData\Local\devmodels`,
			CacheDir:  `C:\Users\u\AppData\Local\devmodels\cache`,
		}},
		{"linux", nil, "/home/u", Paths{
			ConfigDir: "/home/u/.config/devmodels",
			DataDir:   "/home/u/.local/share/devmodels",
			CacheDir:  "/home/u/.cache/devmodels",
		}},
		{"linux", map[string]string{"XDG_CONFIG_HOME": "/x/config", "XDG_DATA_HOME": "/x/data", "XDG_CACHE_HOME": "relative/ignored"}, "/home/u", Paths{
			ConfigDir: "/x/config/devmodels",
			DataDir:   "/x/data/devmodels",
			CacheDir:  "/home/u/.cache/devmodels",
		}},
	}
	for _, c := range cases {
		got, err := ResolvePaths(c.goos, env(c.env), c.home)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s %v:\n got %+v\nwant %+v", c.goos, c.env, got, c.want)
		}
	}
	if _, err := ResolvePaths("linux", env(nil), ""); err == nil {
		t.Error("no home directory must fail")
	}
}

func TestDatabasePathPrecedence(t *testing.T) {
	dir := t.TempDir()
	paths := Paths{ConfigDir: dir, DataDir: filepath.Join(dir, "data"), CacheDir: filepath.Join(dir, "cache")}
	cfgFile := filepath.Join(dir, "config.json")
	vars := map[string]string{EnvConfig: cfgFile}

	r, err := resolve(paths, "", env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if r.DBPath != filepath.Join(dir, "data", DatabaseFileName) || r.DBPathSource != "default" {
		t.Fatalf("default: %+v", r)
	}

	if err := WriteFile(cfgFile, File{DBPath: filepath.Join(dir, "from-file.db")}); err != nil {
		t.Fatal(err)
	}
	if r, _ = resolve(paths, "", env(vars)); r.DBPath != filepath.Join(dir, "from-file.db") || r.DBPathSource != "config file" {
		t.Fatalf("config file: %+v", r)
	}

	vars[EnvDB] = filepath.Join(dir, "from-env.db")
	if r, _ = resolve(paths, "", env(vars)); r.DBPath != vars[EnvDB] {
		t.Fatalf("environment: %+v", r)
	}

	flag := filepath.Join(dir, "from-flag.db")
	if r, _ = resolve(paths, flag, env(vars)); r.DBPath != flag || r.DBPathSource != "flag --db" {
		t.Fatalf("flag: %+v", r)
	}
	if r.BrowserDir != filepath.Join(dir, "cache", "browser") {
		t.Fatalf("browser runtime belongs in the cache dir: %s", r.BrowserDir)
	}
}
