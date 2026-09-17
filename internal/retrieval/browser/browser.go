// Package browser provisions and drives a headless browser for collectors whose
// source only yields its data after JavaScript executes or after interaction.
//
// The user never installs a browser, Node, npm or Playwright. On first use the
// manager downloads a pinned Chrome for Testing "chrome-headless-shell" build
// for the current platform into the per-user cache, verifies the executable is
// present, records a manifest, and removes builds of other versions. Sessions
// drive that executable over the Chrome DevTools Protocol with chromedp, a
// pure-Go client, and expose only the small retrieval.Page interface.
//
// Browser automation is only a retrieval mechanism. It is never a reason to
// collect a source whose terms prohibit automated access.
package browser

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/retrieval"
)

// PinnedVersion is the Chrome for Testing build this release uses. Changing it
// is how the runtime is upgraded: the next use installs the new build and
// removes the old one.
const PinnedVersion = "153.0.8010.36"

// DefaultBaseURL is the Chrome for Testing public download root.
const DefaultBaseURL = "https://storage.googleapis.com/chrome-for-testing-public"

// DefaultMaxDownload bounds the archive download.
const DefaultMaxDownload = 512 << 20

const manifestName = ".devmodels-runtime.json"

// selfTestHTML is a page with a delayed, click-driven state change, used to
// check navigation, interaction, waiting and evaluation end to end without
// contacting any site.
const selfTestHTML = `<!doctype html><title>devmodels browser self-test</title>` +
	`<body data-state="ready"><button id="go">go</button><div id="out"></div>` +
	`<script>document.getElementById("go").addEventListener("click", function () {` +
	`setTimeout(function () { document.getElementById("out").textContent = "clicked"; document.body.dataset.state = "done"; }, 50);` +
	`});</script></body>`

// SelfTestURL is a data: URL of the self-test page.
var SelfTestURL = "data:text/html;charset=utf-8," + url.PathEscape(selfTestHTML)

// Manager owns the browser runtime under Root.
type Manager struct {
	Root        string
	Version     string
	BaseURL     string
	Platform    string
	HTTP        *http.Client
	MaxDownload int64
	// StartTimeout bounds launching the browser and connecting to it.
	StartTimeout time.Duration
	// ActionTimeout bounds each page operation.
	ActionTimeout time.Duration
	Now           func() time.Time
}

var _ retrieval.Browser = (*Manager)(nil)

// NewManager returns a manager for the current platform rooted at root.
func NewManager(root string) *Manager {
	platform, _ := PlatformFor(runtime.GOOS, runtime.GOARCH)
	return &Manager{
		Root:          root,
		Version:       PinnedVersion,
		BaseURL:       DefaultBaseURL,
		Platform:      platform,
		HTTP:          &http.Client{Timeout: 15 * time.Minute},
		MaxDownload:   DefaultMaxDownload,
		StartTimeout:  60 * time.Second,
		ActionTimeout: 30 * time.Second,
		Now:           func() time.Time { return time.Now().UTC() },
	}
}

// PlatformFor maps a Go platform to a Chrome for Testing platform name. Chrome
// for Testing publishes builds only for these platforms; Linux arm64 and
// Windows arm64 have none.
func PlatformFor(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return "mac-arm64", nil
	case "darwin/amd64":
		return "mac-x64", nil
	case "linux/amd64":
		return "linux64", nil
	case "windows/amd64":
		return "win64", nil
	case "windows/386":
		return "win32", nil
	}
	return "", fmt.Errorf("no Chrome for Testing build is published for %s/%s", goos, goarch)
}

// Status describes the runtime on disk.
type Status struct {
	Platform      string   `json:"platform"`
	PinnedVersion string   `json:"pinned_version"`
	Root          string   `json:"root"`
	Provisioned   bool     `json:"provisioned"`
	Executable    string   `json:"executable,omitempty"`
	InstalledAt   string   `json:"installed_at,omitempty"`
	ArchiveSHA256 string   `json:"archive_sha256,omitempty"`
	StaleVersions []string `json:"stale_versions,omitempty"`
	Detail        string   `json:"detail"`
}

type manifest struct {
	Version       string `json:"version"`
	Platform      string `json:"platform"`
	URL           string `json:"url"`
	ArchiveSHA256 string `json:"archive_sha256"`
	Executable    string `json:"executable"`
	InstalledAt   string `json:"installed_at"`
}

func (m *Manager) archiveName() string { return "chrome-headless-shell-" + m.Platform + ".zip" }

// ArchiveURL is the download location for the pinned build.
func (m *Manager) ArchiveURL() string {
	return strings.TrimRight(m.BaseURL, "/") + "/" + m.Version + "/" + m.Platform + "/" + m.archiveName()
}

func (m *Manager) installDir() string { return filepath.Join(m.Root, m.Version, m.Platform) }

func (m *Manager) executableRel() string {
	name := "chrome-headless-shell"
	if strings.HasPrefix(m.Platform, "win") {
		name += ".exe"
	}
	return filepath.Join("chrome-headless-shell-"+m.Platform, name)
}

// Status inspects the runtime without changing anything.
func (m *Manager) Status() Status {
	st := Status{Platform: m.Platform, PinnedVersion: m.Version, Root: m.Root}
	if m.Platform == "" {
		st.Detail = fmt.Sprintf("no browser runtime is available for %s/%s", runtime.GOOS, runtime.GOARCH)
		return st
	}
	st.StaleVersions = m.staleVersions()

	raw, err := os.ReadFile(filepath.Join(m.installDir(), manifestName))
	if err != nil {
		st.Detail = "not provisioned; it is downloaded automatically the first time it is needed (or run `devmodels browser install`)"
		return st
	}
	var man manifest
	if err := json.Unmarshal(raw, &man); err != nil || man.Version != m.Version || man.Platform != m.Platform {
		st.Detail = "the runtime manifest is unreadable or for another build; it will be reinstalled on next use"
		return st
	}
	exe := filepath.Join(m.installDir(), man.Executable)
	if info, err := os.Stat(exe); err != nil || info.IsDir() {
		st.Detail = "the runtime executable is missing; it will be reinstalled on next use"
		return st
	}
	st.Provisioned = true
	st.Executable = exe
	st.InstalledAt = man.InstalledAt
	st.ArchiveSHA256 = man.ArchiveSHA256
	st.Detail = "provisioned"
	return st
}

func (m *Manager) staleVersions() []string {
	entries, err := os.ReadDir(m.Root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != m.Version {
			out = append(out, e.Name())
		}
	}
	return out
}

// Ensure provisions the pinned runtime if it is not already present.
func (m *Manager) Ensure(ctx context.Context) (Status, error) {
	if m.Platform == "" {
		return m.Status(), fmt.Errorf("no browser runtime is available for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if m.Status().Provisioned {
		return m.Status(), nil
	}
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return m.Status(), fmt.Errorf("creating browser runtime directory: %w", err)
	}

	unlock, err := m.lock(ctx)
	if err != nil {
		return m.Status(), err
	}
	defer unlock()

	// Another process may have installed it while this one waited for the lock.
	if m.Status().Provisioned {
		return m.Status(), nil
	}

	archive, sum, err := m.download(ctx)
	if err != nil {
		return m.Status(), err
	}
	defer os.Remove(archive)

	staging, err := os.MkdirTemp(m.Root, ".extract-")
	if err != nil {
		return m.Status(), fmt.Errorf("creating staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	if err := extract(archive, staging); err != nil {
		return m.Status(), err
	}
	exe := filepath.Join(staging, m.executableRel())
	if info, err := os.Stat(exe); err != nil || info.IsDir() {
		return m.Status(), fmt.Errorf("the downloaded archive does not contain %s", m.executableRel())
	}
	if !strings.HasPrefix(m.Platform, "win") {
		if err := os.Chmod(exe, 0o755); err != nil {
			return m.Status(), fmt.Errorf("marking the browser executable: %w", err)
		}
	}

	now := time.Now().UTC()
	if m.Now != nil {
		now = m.Now()
	}
	man, _ := json.MarshalIndent(manifest{
		Version: m.Version, Platform: m.Platform, URL: m.ArchiveURL(), ArchiveSHA256: sum,
		Executable: m.executableRel(), InstalledAt: now.Format(time.RFC3339),
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(staging, manifestName), man, 0o644); err != nil {
		return m.Status(), fmt.Errorf("writing runtime manifest: %w", err)
	}

	if err := os.RemoveAll(m.installDir()); err != nil {
		return m.Status(), fmt.Errorf("clearing a previous partial install: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.installDir()), 0o755); err != nil {
		return m.Status(), err
	}
	if err := os.Rename(staging, m.installDir()); err != nil {
		return m.Status(), fmt.Errorf("activating the browser runtime: %w", err)
	}
	m.removeStale()
	return m.Status(), nil
}

// removeStale deletes builds of other versions. It runs only after this
// version was installed, under the install lock, and is best effort: a process
// still running an older build (an MCP server during an upgrade) may hold its
// files open, notably on Windows, and a leftover build is reported by Status.
func (m *Manager) removeStale() {
	for _, v := range m.staleVersions() {
		os.RemoveAll(filepath.Join(m.Root, v))
	}
}

// The install lock's holder refreshes its modification time while it
// installs, so a lock left unrefreshed for lockStaleAfter was left by a process
// that died and can be taken over. Two waiters can take over the same stale
// lock at once; both then install, and the later activation fails or replaces
// an identical build.
const (
	lockRefresh    = 20 * time.Second
	lockStaleAfter = 2 * time.Minute
)

// lock takes the install lock, waiting while another process installs. The
// returned func stops refreshing the lock and releases it only if this process
// still holds it.
func (m *Manager) lock(ctx context.Context) (func(), error) {
	path := filepath.Join(m.Root, ".install.lock")
	token := fmt.Sprintf("%d %d\n", os.Getpid(), time.Now().UnixNano())
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := f.WriteString(token)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				os.Remove(path)
				return nil, fmt.Errorf("locking the browser runtime: %w", werr)
			}
			done := make(chan struct{})
			go func() {
				tick := time.NewTicker(lockRefresh)
				defer tick.Stop()
				for {
					select {
					case <-done:
						return
					case now := <-tick.C:
						os.Chtimes(path, now, now)
					}
				}
			}()
			return func() {
				close(done)
				if held, err := os.ReadFile(path); err == nil && string(held) == token {
					os.Remove(path)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("locking the browser runtime: %w", err)
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > lockStaleAfter {
			os.Remove(path)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for another process to finish installing the browser runtime (lock %s): %w", path, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (m *Manager) download(ctx context.Context) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.ArchiveURL(), nil)
	if err != nil {
		return "", "", err
	}
	client := m.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("downloading the browser runtime: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("downloading the browser runtime from %s: unexpected status %s", m.ArchiveURL(), resp.Status)
	}

	f, err := os.CreateTemp(m.Root, ".download-*.zip")
	if err != nil {
		return "", "", err
	}
	limit := m.MaxDownload
	if limit <= 0 {
		limit = DefaultMaxDownload
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, limit+1))
	closeErr := f.Close()
	fail := func(err error) (string, string, error) {
		os.Remove(f.Name())
		return "", "", err
	}
	switch {
	case copyErr != nil:
		return fail(fmt.Errorf("downloading the browser runtime: %w", copyErr))
	case closeErr != nil:
		return fail(closeErr)
	case n > limit:
		return fail(fmt.Errorf("the browser runtime download exceeds %d bytes", limit))
	case resp.ContentLength > 0 && n != resp.ContentLength:
		return fail(fmt.Errorf("the browser runtime download is incomplete: %d of %d bytes", n, resp.ContentLength))
	}
	return f.Name(), hex.EncodeToString(hash.Sum(nil)), nil
}

// extract unpacks archive into dest. Every directory, link and file is created
// through an os.Root, which refuses to follow a path or an earlier link out of
// dest: checking names alone cannot catch links that chain through links
// created by earlier entries. The name checks still reject unsafe entries up
// front with a clear message.
func extract(archive, dest string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("opening the browser runtime archive: %w", err)
	}
	defer r.Close()

	root, err := os.OpenRoot(dest)
	if err != nil {
		return fmt.Errorf("opening the browser runtime staging directory: %w", err)
	}
	defer root.Close()

	prefix := filepath.Clean(dest) + string(os.PathSeparator)
	for _, f := range r.File {
		target := filepath.Join(dest, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(target, prefix) {
			return fmt.Errorf("the browser runtime archive contains an unsafe path %q", f.Name)
		}
		rel := strings.TrimPrefix(target, prefix)
		switch {
		case f.FileInfo().IsDir():
			if err := root.MkdirAll(rel, 0o755); err != nil {
				return fmt.Errorf("extracting %q: %w", f.Name, err)
			}
		case f.Mode()&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			raw, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return err
			}
			link := filepath.FromSlash(string(raw))
			resolved := filepath.Join(filepath.Dir(target), link)
			if filepath.IsAbs(link) || filepath.VolumeName(link) != "" || strings.HasPrefix(link, string(os.PathSeparator)) || !strings.HasPrefix(resolved, prefix) {
				return fmt.Errorf("the browser runtime archive contains an unsafe link %q", f.Name)
			}
			if err := mkdirParent(root, rel); err != nil {
				return fmt.Errorf("extracting %q: %w", f.Name, err)
			}
			if err := root.Symlink(link, rel); err != nil {
				return fmt.Errorf("extracting %q: %w", f.Name, err)
			}
		default:
			if err := writeFile(root, f, rel); err != nil {
				return fmt.Errorf("extracting %q: %w", f.Name, err)
			}
		}
	}
	return nil
}

func mkdirParent(root *os.Root, rel string) error {
	if dir := filepath.Dir(rel); dir != "." {
		return root.MkdirAll(dir, 0o755)
	}
	return nil
}

func writeFile(root *os.Root, f *zip.File, rel string) error {
	if err := mkdirParent(root, rel); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := f.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	out, err := root.OpenFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// WithPage provisions the runtime if needed, launches it with a throwaway
// profile, and runs fn against one page. The browser process and profile are
// removed when fn returns or ctx is cancelled.
func (m *Manager) WithPage(ctx context.Context, fn func(retrieval.Page) error) error {
	st, err := m.Ensure(ctx)
	if err != nil {
		return err
	}
	profile, err := os.MkdirTemp("", "devmodels-browser-profile-")
	if err != nil {
		return fmt.Errorf("creating a browser profile directory: %w", err)
	}
	defer os.RemoveAll(profile)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(st.Executable),
		chromedp.UserDataDir(profile),
		chromedp.DisableGPU,
	)
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	// The first Run launches the browser. A timeout on that context would kill
	// the browser later, so the launch is bounded from outside instead.
	started := make(chan error, 1)
	go func() { started <- chromedp.Run(browserCtx) }()
	select {
	case err := <-started:
		if err != nil {
			return fmt.Errorf("starting the browser runtime (%s): %w", st.Executable, err)
		}
	case <-time.After(m.startTimeout()):
		return fmt.Errorf("starting the browser runtime (%s): no DevTools connection within %s", st.Executable, m.startTimeout())
	case <-ctx.Done():
		return ctx.Err()
	}

	return fn(&page{ctx: browserCtx, timeout: m.actionTimeout()})
}

func (m *Manager) startTimeout() time.Duration {
	if m.StartTimeout > 0 {
		return m.StartTimeout
	}
	return 60 * time.Second
}

func (m *Manager) actionTimeout() time.Duration {
	if m.ActionTimeout > 0 {
		return m.ActionTimeout
	}
	return 30 * time.Second
}

// page implements retrieval.Page over a chromedp tab.
type page struct {
	ctx     context.Context
	timeout time.Duration
}

func (p *page) run(what string, actions ...chromedp.Action) error {
	ctx, cancel := context.WithTimeout(p.ctx, p.timeout)
	defer cancel()
	if err := chromedp.Run(ctx, actions...); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%s: timed out after %s", what, p.timeout)
		}
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func (p *page) Navigate(target string) error {
	return p.run("navigating to "+target, chromedp.Navigate(target))
}

func (p *page) WaitVisible(selector string) error {
	return p.run("waiting for "+selector+" to be visible", chromedp.WaitVisible(selector, chromedp.ByQuery))
}

func (p *page) WaitFor(expression string) error {
	deadline := time.Now().Add(p.timeout)
	for {
		var ok bool
		if err := p.run("evaluating wait condition "+expression, chromedp.Evaluate("Boolean("+expression+")", &ok)); err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waiting for %s: timed out after %s", expression, p.timeout)
		}
		select {
		case <-p.ctx.Done():
			return p.ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *page) Click(selector string) error {
	return p.run("clicking "+selector, chromedp.Click(selector, chromedp.ByQuery))
}

func (p *page) Evaluate(expression string, out any) error {
	return p.run("evaluating script", chromedp.Evaluate(expression, out))
}

func (p *page) HTML(selector string) (string, error) {
	var html string
	err := p.run("reading HTML of "+selector, chromedp.OuterHTML(selector, &html, chromedp.ByQuery))
	return html, err
}

func (p *page) Text(selector string) (string, error) {
	var text string
	err := p.run("reading text of "+selector, chromedp.Text(selector, &text, chromedp.ByQuery))
	return text, err
}

func (p *page) Attribute(selector, name string) (string, bool, error) {
	var value string
	var ok bool
	err := p.run("reading attribute "+name+" of "+selector, chromedp.AttributeValue(selector, name, &value, &ok, chromedp.ByQuery))
	return value, ok, err
}

// Remove deletes every installed runtime.
func (m *Manager) Remove() error {
	entries, err := os.ReadDir(m.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			if err := os.RemoveAll(filepath.Join(m.Root, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
