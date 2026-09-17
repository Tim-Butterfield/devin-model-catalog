package browser

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/retrieval"
)

const testPlatform = "linux64"

func archive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range entries {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(0o755)
		fw, err := w.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const fakeShell = "#!/bin/sh\necho \"<html><body>$*</body></html>\"\n"

func goodArchive(t *testing.T) []byte {
	return archive(t, map[string]string{"chrome-headless-shell-" + testPlatform + "/chrome-headless-shell": fakeShell})
}

type fakeServer struct {
	*httptest.Server
	hits    atomic.Int32
	archive []byte
	status  int
}

func serve(t *testing.T, body []byte) *fakeServer {
	fs := &fakeServer{archive: body, status: http.StatusOK}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.hits.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/"+testPlatform+"/chrome-headless-shell-"+testPlatform+".zip") {
			http.NotFound(w, r)
			return
		}
		if fs.status != http.StatusOK {
			w.WriteHeader(fs.status)
			return
		}
		w.Write(fs.archive)
	}))
	t.Cleanup(fs.Close)
	return fs
}

func manager(root, version string, srv *fakeServer) *Manager {
	m := NewManager(root)
	m.Version, m.BaseURL, m.Platform, m.HTTP = version, srv.URL, testPlatform, srv.Client()
	return m
}

func TestProvisionOnceAndReuse(t *testing.T) {
	srv := serve(t, goodArchive(t))
	m := manager(t.TempDir(), "1.0.0", srv)

	if st := m.Status(); st.Provisioned {
		t.Fatal("fresh cache must not be provisioned")
	}
	st, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Provisioned || st.ArchiveSHA256 == "" || !strings.HasPrefix(st.Executable, m.Root) {
		t.Fatalf("status after install: %+v", st)
	}
	if _, err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("an installed runtime must not be downloaded again; %d downloads", n)
	}
	if _, err := os.Stat(filepath.Join(m.Root, ".install.lock")); !os.IsNotExist(err) {
		t.Fatal("the install lock must be released")
	}
}

func TestSessionStartFailureIsDiagnosable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake runtime is a shell script")
	}
	srv := serve(t, goodArchive(t))
	m := manager(t.TempDir(), "1.0.0", srv)
	m.StartTimeout = 10 * time.Second
	called := false
	err := m.WithPage(context.Background(), func(retrieval.Page) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("a runtime that cannot speak DevTools must fail before the collector runs: err=%v called=%v", err, called)
	}
	if !strings.Contains(err.Error(), "starting the browser runtime") || !strings.Contains(err.Error(), "chrome-headless-shell") {
		t.Fatalf("the error should name the step and the executable: %v", err)
	}
	if st := m.Status(); !st.Provisioned {
		t.Fatal("a launch failure must not corrupt the installed runtime state")
	}
}

func TestUpgradeRemovesStaleVersions(t *testing.T) {
	srv := serve(t, goodArchive(t))
	root := t.TempDir()
	if _, err := manager(root, "1.0.0", srv).Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := manager(root, "2.0.0", srv)
	if st := next.Status(); st.Provisioned || len(st.StaleVersions) != 1 {
		t.Fatalf("a pinned-version change must require reinstall and report the stale build: %+v", st)
	}
	st, err := next.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Provisioned || len(st.StaleVersions) != 0 {
		t.Fatalf("after upgrade: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(root, "1.0.0")); !os.IsNotExist(err) {
		t.Fatal("the stale version must be removed")
	}
}

// linkArchive is a runtime archive that also contains a symlink entry.
func linkArchive(t *testing.T, name, target string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	link := &zip.FileHeader{Name: name, Method: zip.Store}
	link.SetMode(os.ModeSymlink | 0o777)
	lw, err := w.CreateHeader(link)
	if err != nil {
		t.Fatal(err)
	}
	lw.Write([]byte(target))
	exe := &zip.FileHeader{Name: "chrome-headless-shell-" + testPlatform + "/chrome-headless-shell", Method: zip.Deflate}
	exe.SetMode(0o755)
	ew, err := w.CreateHeader(exe)
	if err != nil {
		t.Fatal(err)
	}
	ew.Write([]byte(fakeShell))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// chainArchive links a directory to its parent, then creates a second link
// through the first. Each link looks safe by name alone; together they point
// above the staging directory, and the last entry would be written there.
func chainArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	add := func(name string, mode os.FileMode, body string) {
		hdr := &zip.FileHeader{Name: name, Method: zip.Store}
		hdr.SetMode(mode)
		fw, err := w.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte(body))
	}
	add("a/b/", os.ModeDir|0o755, "")
	add("a/b/l", os.ModeSymlink|0o777, "..")
	add("a/b/l/m", os.ModeSymlink|0o777, "../..")
	add("a/b/l/m/escaped", 0o644, "x")
	add("chrome-headless-shell-"+testPlatform+"/chrome-headless-shell", 0o755, fakeShell)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestChainedLinksCannotEscapeTheInstallDirectory(t *testing.T) {
	srv := serve(t, chainArchive(t))
	root := t.TempDir()
	m := manager(root, "1.0.0", srv)
	if _, err := m.Ensure(context.Background()); err == nil {
		t.Fatal("an archive whose links chain out of the staging directory must fail")
	}
	for _, escaped := range []string{filepath.Join(root, "escaped"), filepath.Join(filepath.Dir(root), "escaped")} {
		if _, err := os.Stat(escaped); err == nil {
			t.Fatalf("an entry was written outside the staging directory: %s", escaped)
		}
	}
	if m.Status().Provisioned {
		t.Fatal("a failed install must not be active")
	}
}

func TestFailedInstallsLeaveNothingActive(t *testing.T) {
	cases := map[string]func(*fakeServer){
		"server error":       func(s *fakeServer) { s.status = http.StatusInternalServerError },
		"corrupt archive":    func(s *fakeServer) { s.archive = []byte("not a zip") },
		"missing executable": func(s *fakeServer) { s.archive = archive(t, map[string]string{"README": "x"}) },
		"unsafe path": func(s *fakeServer) {
			s.archive = archive(t, map[string]string{"../escape": "x", "chrome-headless-shell-" + testPlatform + "/chrome-headless-shell": fakeShell})
		},
		"absolute link": func(s *fakeServer) {
			s.archive = linkArchive(t, "chrome-headless-shell-"+testPlatform+"/lib", string(filepath.Separator)+"tmp")
		},
		"escaping link": func(s *fakeServer) {
			s.archive = linkArchive(t, "chrome-headless-shell-"+testPlatform+"/lib", "../../..")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			srv := serve(t, goodArchive(t))
			mutate(srv)
			root := t.TempDir()
			m := manager(root, "1.0.0", srv)
			if _, err := m.Ensure(context.Background()); err == nil {
				t.Fatal("expected failure")
			}
			if m.Status().Provisioned {
				t.Fatal("a failed install must not be active")
			}
			entries, _ := os.ReadDir(root)
			for _, e := range entries {
				t.Errorf("left behind: %s", e.Name())
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
				t.Fatal("zip entry escaped the staging directory")
			}
		})
	}
}

func TestPlatformFor(t *testing.T) {
	cases := map[string]string{
		"darwin/arm64": "mac-arm64", "darwin/amd64": "mac-x64", "linux/amd64": "linux64",
		"windows/amd64": "win64", "windows/386": "win32",
	}
	for in, want := range cases {
		goos, goarch, _ := strings.Cut(in, "/")
		if got, err := PlatformFor(goos, goarch); err != nil || got != want {
			t.Errorf("%s = %q %v", in, got, err)
		}
	}
	// No Chrome for Testing build is published for these.
	for _, in := range []string{"linux/arm64", "windows/arm64", "plan9/amd64"} {
		goos, goarch, _ := strings.Cut(in, "/")
		if got, err := PlatformFor(goos, goarch); err == nil {
			t.Errorf("%s must be unsupported, got %q", in, got)
		}
	}
	win := &Manager{Platform: "win64"}
	if !strings.HasSuffix(win.executableRel(), "chrome-headless-shell.exe") {
		t.Error("windows executable needs .exe")
	}
}

func TestInstallWaitsForAnotherProcessAndHonoursCancellation(t *testing.T) {
	srv := serve(t, goodArchive(t))
	m := manager(t.TempDir(), "1.0.0", srv)
	lock := filepath.Join(m.Root, ".install.lock")
	if err := os.WriteFile(lock, []byte("another process\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_, err := m.Ensure(ctx)
	if err == nil || !strings.Contains(err.Error(), "waiting for another process") {
		t.Fatalf("a held lock must be waited for until cancellation: %v", err)
	}
	if held, _ := os.ReadFile(lock); string(held) != "another process\n" {
		t.Fatal("a lock held by another process must not be removed")
	}

	// A lock its holder stopped refreshing was left by a process that died.
	stale := time.Now().Add(-2 * lockStaleAfter)
	if err := os.Chtimes(lock, stale, stale); err != nil {
		t.Fatal(err)
	}
	quick, cancelQuick := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelQuick()
	if st, err := m.Ensure(quick); err != nil || !st.Provisioned {
		t.Fatalf("a stale lock must be taken over: %+v %v", st, err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("the taken-over lock must be released")
	}
	os.RemoveAll(filepath.Join(m.Root, "1.0.0"))
	if err := os.WriteFile(lock, []byte("another process\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Once the other process releases the lock, the install proceeds.
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.Remove(lock)
	}()
	if st, err := m.Ensure(context.Background()); err != nil || !st.Provisioned {
		t.Fatalf("install after the lock is released: %+v %v", st, err)
	}
}
