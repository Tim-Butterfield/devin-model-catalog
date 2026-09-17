package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/config"
)

func TestWriteFileReplacesContentAtomicallyAndKeepsALink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "config.json")
	if err := config.WriteFile(real, config.File{DBPath: "/first.db"}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(real); err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("config permissions: %v %v", info.Mode().Perm(), err)
		}
	}

	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := config.WriteFile(link, config.File{DBPath: "/second.db"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("writing through a symlinked config must keep the link: %v", err)
	}
	got, err := config.ReadFile(real)
	if err != nil || got.DBPath != "/second.db" {
		t.Fatalf("the link's target must hold the new config: %+v %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(real))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}
