//go:build browser

package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/retrieval"
)

// TestRealBrowserRuntime downloads the pinned Chrome for Testing build and
// drives it against a local page. It needs network access to Google's
// Chrome for Testing storage. Run with:
//
//	go test ./internal/retrieval/browser -tags=browser -run TestRealBrowserRuntime -v
//
// Set DEVMODELS_BROWSER_TEST_ROOT to reuse a runtime directory between runs.
func TestRealBrowserRuntime(t *testing.T) {
	root := os.Getenv("DEVMODELS_BROWSER_TEST_ROOT")
	if root == "" {
		root = t.TempDir()
	}
	m := NewManager(root)
	if m.Platform == "" {
		t.Skip("no Chrome for Testing build for this platform")
	}
	m.ActionTimeout = 10 * time.Second
	ctx := context.Background()

	before := m.Status()
	began := time.Now()
	st, err := m.Ensure(ctx)
	if err != nil {
		t.Fatalf("provisioning: %v", err)
	}
	t.Logf("provisioned before=%v; ensure took %s; executable=%s sha256=%s", before.Provisioned, time.Since(began).Round(time.Millisecond), st.Executable, st.ArchiveSHA256)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"name":"Model A","credits":3},{"name":"Model B","credits":9}]`))
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(interactivePage))
		}
	}))
	defer srv.Close()

	type row struct {
		Name    string  `json:"name"`
		Credits float64 `json:"credits"`
	}
	err = m.WithPage(ctx, func(p retrieval.Page) error {
		if err := p.Navigate(srv.URL); err != nil {
			return err
		}
		// Rows are rendered client-side after a delayed fetch.
		if err := p.WaitVisible("#rows li"); err != nil {
			return err
		}
		var rows []row
		if err := p.Evaluate(`Array.from(document.querySelectorAll("#rows li")).map(li => ({name: li.dataset.name, credits: Number(li.dataset.credits)}))`, &rows); err != nil {
			return err
		}
		if len(rows) != 2 || rows[1].Name != "Model B" || rows[1].Credits != 9 {
			t.Errorf("evaluated rows: %+v", rows)
		}

		// A hidden panel appears only after a click and a delay.
		if err := p.Click("#tab-legacy"); err != nil {
			return err
		}
		if err := p.WaitFor(`document.querySelector("#panel-legacy").hidden === false`); err != nil {
			return err
		}
		text, err := p.Text("#panel-legacy")
		if err != nil {
			return err
		}
		if !strings.Contains(text, "legacy panel") {
			t.Errorf("panel text: %q", text)
		}
		if v, ok, err := p.Attribute("#panel-legacy", "data-rows"); err != nil || !ok || v != "2" {
			t.Errorf("attribute: %q %v %v", v, ok, err)
		}
		if html, err := p.HTML("#panel-legacy"); err != nil || !strings.HasPrefix(html, "<section") {
			t.Errorf("html: %q %v", html, err)
		}

		// A condition that never holds fails within the action timeout.
		waitStart := time.Now()
		if err := p.WaitFor(`window.neverTrue === true`); err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Errorf("unsatisfiable wait: %v", err)
		}
		if took := time.Since(waitStart); took > m.ActionTimeout+5*time.Second {
			t.Errorf("wait took %s", took)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	// Reuse: no second download, same install.
	began = time.Now()
	again, err := m.Ensure(ctx)
	if err != nil || again.InstalledAt != st.InstalledAt || again.ArchiveSHA256 != st.ArchiveSHA256 {
		t.Fatalf("reuse: %+v %v", again, err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("reusing an installed runtime took %s", took)
	}
	err = m.WithPage(ctx, func(p retrieval.Page) error {
		if err := p.Navigate(SelfTestURL); err != nil {
			return err
		}
		if err := p.Click("#go"); err != nil {
			return err
		}
		if err := p.WaitFor(`document.body.dataset.state === "done"`); err != nil {
			return err
		}
		var sum int
		return p.Evaluate(`20 + 22`, &sum)
	})
	if err != nil {
		t.Fatalf("second session: %v", err)
	}

	// A failed upgrade leaves the working install in place and diagnosable.
	bad := NewManager(root)
	bad.Version = "0.0.0.0"
	if _, err := bad.Ensure(ctx); err == nil {
		t.Fatal("a non-existent pinned version must fail")
	} else {
		t.Logf("failed upgrade reported: %v", err)
	}
	if st := bad.Status(); st.Provisioned {
		t.Fatal("the failed version must not report as provisioned")
	}
	if st := m.Status(); !st.Provisioned {
		t.Fatal("a failed upgrade must leave the working runtime intact")
	}
	if _, err := os.Stat(root + "/.install.lock"); !os.IsNotExist(err) {
		t.Fatal("the install lock must be released after a failure")
	}
}

const interactivePage = `<!doctype html>
<html><head><title>interactive fixture</title></head>
<body>
<ul id="rows"></ul>
<button id="tab-legacy">Legacy</button>
<section id="panel-legacy" hidden data-rows="0">legacy panel</section>
<script>
setTimeout(function () {
  fetch("/data").then(function (r) { return r.json(); }).then(function (rows) {
    var list = document.getElementById("rows");
    rows.forEach(function (row) {
      var li = document.createElement("li");
      li.dataset.name = row.name;
      li.dataset.credits = row.credits;
      li.textContent = row.name;
      list.appendChild(li);
    });
    document.getElementById("panel-legacy").dataset.rows = String(rows.length);
  });
}, 300);
document.getElementById("tab-legacy").addEventListener("click", function () {
  setTimeout(function () { document.getElementById("panel-legacy").hidden = false; }, 200);
});
</script>
</body></html>`
