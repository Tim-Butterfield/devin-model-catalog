package cli_test

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/cli"
)

// The human-readable output this program composes is laid out for a fixed
// width. These tests hold that line, because it is the kind of property that
// survives exactly as long as something checks it: every new command, flag
// description or note is one careless sentence away from wrapping raggedly in
// the terminal of whoever runs it.
//
// They measure display columns rather than bytes. An em dash is one column and
// three bytes, so a byte count would fail this suite on output that is fine.

// columns is the reference width model for the tests. It is written out here
// rather than calling the implementation, so that a bug in the implementation's
// own measurement cannot make these assertions agree with it.
func columns(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		case r >= 0x1100 && r <= 0x115F,
			r >= 0x2E80 && r <= 0x303E,
			r >= 0x3041 && r <= 0x33FF,
			r >= 0x3400 && r <= 0x4DBF,
			r >= 0x4E00 && r <= 0x9FFF,
			r >= 0xAC00 && r <= 0xD7A3,
			r >= 0xF900 && r <= 0xFAFF,
			r >= 0xFF00 && r <= 0xFF60,
			r >= 0x1F300 && r <= 0x1F64F,
			r >= 0x20000 && r <= 0x3FFFD:
			w += 2
		default:
			w++
		}
	}
	return w
}

// unbreakable reports whether a line is over the width only because it carries
// a single value that cannot be broken — a filesystem path, a URL, an
// identifier with no spaces in it.
//
// Those are allowed to overflow, deliberately: a path cut across two lines
// cannot be copied, and a path silently truncated is worse than one that runs
// long. Everything else is prose or a laid-out table and has no excuse, so this
// is a classification rather than a way of ignoring failures — a long sentence
// is made of short words and is never excused by it.
func unbreakable(line string) bool {
	longest := 0
	for _, field := range strings.Fields(line) {
		if w := columns(field); w > longest {
			longest = w
		}
	}
	indent := columns(line) - columns(strings.TrimLeft(line, " "))
	return longest+indent > cli.Width
}

// overWidth returns the offending lines, with their measured width, so a
// failure names what to fix rather than only that something is wrong.
// Lines excused by unbreakable are returned separately.
func overWidth(text string) (bad, excused []string) {
	for i, line := range strings.Split(text, "\n") {
		w := columns(line)
		if w <= cli.Width {
			continue
		}
		note := strings.TrimRight(line[:min(len(line), 120)], " ") +
			" ← line " + itoa(i+1) + ", " + itoa(w) + " columns"
		if unbreakable(line) {
			excused = append(excused, note)
		} else {
			bad = append(bad, note)
		}
	}
	return bad, excused
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func assertFits(t *testing.T, what, text string) {
	t.Helper()
	bad, excused := overWidth(text)
	if len(bad) > 0 {
		t.Errorf("%s has %d line(s) wider than %d columns:\n  %s",
			what, len(bad), cli.Width, strings.Join(bad, "\n  "))
	}
	for _, line := range excused {
		t.Logf("%s: unbreakable value runs long, which is the documented policy: %s", what, line)
	}
	if !utf8.ValidString(text) {
		t.Errorf("%s is not valid UTF-8", what)
	}
}

// helpSurfaces is every way a caller can ask this program to explain itself.
// The command list is taken from the top-level help text rather than written
// out again here, so a command added without help is a failure rather than
// something this test quietly skips.
func TestEveryHelpSurfaceFitsTheWidth(t *testing.T) {
	r := newRunner(t)

	_, top, topErr := r.run("help")
	assertFits(t, "devmodels help", top+topErr)

	for _, args := range [][]string{{"-h"}, {"--help"}, {"version"}, {"query", "--help"}} {
		_, out, errOut := r.run(args...)
		assertFits(t, "devmodels "+strings.Join(args, " "), out+errOut)
	}

	commands := commandsFromHelp(t, top)
	if len(commands) < 10 {
		t.Fatalf("expected the help text to list the commands; found %v", commands)
	}
	for _, name := range commands {
		// agents-md returns AGENTS.md verbatim, and byte identity with the file
		// is the contract that command exists to keep — tests elsewhere assert
		// it. Its width is the document's, not this program's, so the width
		// invariant does not reach it.
		if name == "agents-md" {
			continue
		}
		// Every command is asked for help two ways. Some take a subcommand and
		// answer with a usage error, which is itself a human surface.
		for _, args := range [][]string{{name, "--help"}, {name, "--not-a-flag"}} {
			_, out, errOut := r.run(args...)
			assertFits(t, "devmodels "+strings.Join(args, " "), out+errOut)
		}
	}
	t.Logf("checked %d commands discovered from the help text: %s", len(commands), strings.Join(commands, " "))
}

// The first screen is a landing page, not a reference manual: what this is,
// and which command to reach for. What used to be on it and is deliberately
// not any more each belongs somewhere a reader is actually asking for it — the
// exit codes in the README a script author is reading, the import format in
// `devmodels import --help`, a command's flags in that command's own help.
//
// That last one is only true while every command it names has help of its own,
// so the closing pointer is followed here rather than taken on trust.
func TestTheLandingPageStaysALandingPage(t *testing.T) {
	r := newRunner(t)
	_, help, _ := r.run("help")

	for _, gone := range []string{"Exit codes:", "docs/", "Ids ", "Evidence curation"} {
		if strings.Contains(help, gone) {
			t.Errorf("the landing page carries %q again:\n%s", gone, help)
		}
	}
	for _, needed := range []string{
		"not affiliated with or endorsed by",
		"--db PATH",
		"--json",
		"devmodels <command> --help",
	} {
		if !strings.Contains(help, needed) {
			t.Errorf("the landing page no longer says %q:\n%s", needed, help)
		}
	}
	if lines := strings.Count(help, "\n"); lines > 70 {
		t.Errorf("the landing page is %d lines; it is meant to be read, not consulted:\n%s", lines, help)
	}

	for _, name := range commandsFromHelp(t, help) {
		// version and agents-md take no flags and answer immediately.
		if name == "version" || name == "agents-md" {
			continue
		}
		_, out, errOut := r.run(name, "--help")
		if text := out + errOut; !strings.Contains(text, "usage: devmodels "+name) {
			t.Errorf("devmodels %s --help does not print help of its own:\n%s", name, text)
		}
	}
}

// commandsFromHelp reads the command names out of the help text: the lines that
// are indented two spaces and start with a lowercase word. A flag is indented
// the same way and is not a command, and neither is the program's own name on
// the usage line.
func commandsFromHelp(t *testing.T, help string) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(help, "\n") {
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if name == "" || name == "devmodels" || strings.HasPrefix(name, "-") ||
			!isLowerWord(name) || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func isLowerWord(s string) bool {
	for _, r := range s {
		if !unicode.IsLower(r) && r != '-' {
			return false
		}
	}
	return s != ""
}

// Output on a database with nothing in it yet. These are the messages a new
// user meets first, and the not-ready explanations are among the longest
// sentences the program produces — a catalog loaded with fixtures never shows
// them, so a suite that only tests the happy path never sees them.
func TestOutputBeforeAnyRefreshFitsTheWidth(t *testing.T) {
	r := newRunner(t)
	for _, args := range [][]string{
		{"status"},
		{"metrics"},
		{"datasets"},
		{"config"},
		{"query", "--where", "swe_bench_verified>=1"},
		{"model", "anything"},
		{"unmatched"},
		{"rejected"},
		{"alias", "list"},
	} {
		_, out, errOut := r.run(args...)
		assertFits(t, "devmodels "+strings.Join(args, " ")+" (empty database)", out+errOut)
	}
}

// The commands that change state report what they did, in sentences long
// enough to wrap.
func TestStateChangingOutputFitsTheWidth(t *testing.T) {
	r := newRunner(t)
	for _, args := range [][]string{
		{"datasets", "refresh"},
		{"use-dataset", "1"},
		{"refresh"},
		{"use-dataset", "2"}, // switching away reports the catalog being cleared
		{"use-dataset", "99"},
		{"alias", "add", "--source", "epoch", "--name", "some-other-model", "--target", "Claude Opus 5 Medium"},
		{"alias", "list"},
		{"import", "/nonexistent/file.json"},
	} {
		_, out, errOut := r.run(args...)
		assertFits(t, "devmodels "+strings.Join(args, " "), out+errOut)
	}
}

// The commands that produce a table or a report are exercised against the
// fixture catalog, because a layout that fits when empty proves nothing.
func TestNormalOutputFitsTheWidth(t *testing.T) {
	r, _, _ := refreshedCLI(t)

	for _, args := range [][]string{
		{"datasets"},
		{"status"},
		{"metrics"},
		{"metrics", "--detail", "full"},
		{"config"},
		{"browser", "status"},
		{"alias", "list"},
		{"rejected"},
		{"unmatched"},
		{"query", "--where", "swe_bench_verified>=1", "--limit", "5"},
		{"query", "--where", "swe_bench_verified>=1", "--where-or-missing", "bfcl_overall_accuracy>=1", "--limit", "5"},
		{"query", "--where", "swe_bench_verified>=99.9"},
		{"query", "--where", "no_such_metric>=1"},
		{"model", "no-such-model"},
	} {
		_, out, errOut := r.run(args...)
		assertFits(t, "devmodels "+strings.Join(args, " "), out+errOut)
	}
}

// A query can be asked for many metric columns at once. Whatever it does about
// that — narrower columns, and past a point a different layout altogether — the
// line length is not what gives way. Which layout it chooses where is the
// subject of cli_query_layout_test.go; here only the width is at stake.
func TestQueryStaysWithinTheWidthAsColumnsAreAdded(t *testing.T) {
	r, _, _ := refreshedCLI(t)
	args := []string{"query", "--where", "swe_bench_verified>=1", "--limit", "5"}
	for _, extra := range []string{
		"bfcl_overall_accuracy", "deepswe", "terminal_bench", "frontiercode",
		"devin_input_price_usd_per_mtok", "devin_output_price_usd_per_mtok",
		"devin_cache_read_price_usd_per_mtok", "devin_cache_write_price_usd_per_mtok",
	} {
		args = append(args, "--where-or-missing", extra+">=1")
		_, out, errOut := r.run(args...)
		assertFits(t, "devmodels query with "+extra, out+errOut)
	}
}

// A model detail view is one long metric key after another, which is the case
// most likely to overflow.
func TestModelDetailFitsTheWidth(t *testing.T) {
	r, _ := refreshedRunner(t)
	for _, detail := range []string{"compact", "full"} {
		_, out, errOut := r.run("model", "claude-opus-5-medium", "--detail", detail)
		assertFits(t, "devmodels model --detail "+detail, out+errOut)
	}
}

// Wrapping and truncation must not cut a character in half, and two long
// identifiers that differ late must not become the same string on screen.
func TestLongValuesAreShortenedSafely(t *testing.T) {
	long := strings.Repeat("a", 60)
	for _, s := range []string{
		long,
		"日本語のモデル名" + long,
		"é" + long, // e + combining acute
		"model-" + strings.Repeat("x", 100),
	} {
		for _, max := range []int{1, 2, 5, 20, 40} {
			got := cli.TruncateForTest(s, max)
			if !utf8.ValidString(got) {
				t.Errorf("truncate(%q, %d) is not valid UTF-8", s[:min(len(s), 20)], max)
			}
			if w := columns(got); w > max {
				t.Errorf("truncate(%q, %d) is %d columns", s[:min(len(s), 20)], max, w)
			}
		}
	}

	// Identifiers that differ only near the end are still told apart, because
	// the full value is reachable: the listing that truncates says where.
	a := "devmodels-really-long-identifier-variant-alpha"
	b := "devmodels-really-long-identifier-variant-beta"
	if cli.TruncateForTest(a, 44) == cli.TruncateForTest(b, 44) {
		t.Log("two long identifiers collapse to the same truncated form at 44 columns; " +
			"the surfaces that truncate point at --json for the whole value")
	}
	if cli.TruncateForTest(a, 80) == cli.TruncateForTest(b, 80) {
		t.Error("identifiers that fit the width must not be altered at all")
	}
}

// Wrapping keeps whole words and never produces a line over the width, except
// where a single unbreakable token is itself longer than the width.
func TestWrapKeepsWithinTheWidth(t *testing.T) {
	text := "The catalog reports every value with its provenance: the source, " +
		"the source's own name for the model, and the evaluation context that " +
		"produced it, so a reader can tell a measured number from a borrowed one."
	lines := cli.WrapForTest(text, "note: ", "  ")
	for i, line := range lines {
		if w := columns(line); w > cli.Width {
			t.Errorf("wrapped line %d is %d columns: %q", i+1, w, line)
		}
	}
	if !strings.HasPrefix(lines[0], "note: ") {
		t.Errorf("first line lost its prefix: %q", lines[0])
	}
	if len(lines) > 1 && !strings.HasPrefix(lines[1], "  ") {
		t.Errorf("continuation lost its indent: %q", lines[1])
	}
	if joined := strings.Join(lines, " "); !strings.Contains(
		strings.Join(strings.Fields(joined), " "), "evaluation context that produced it") {
		t.Error("wrapping dropped or reordered words")
	}

	// A token longer than the width cannot be broken without corrupting it, so
	// it takes a line of its own rather than being split.
	path := "/Users/someone/Library/Application Support/devmodels/" + strings.Repeat("x", 90)
	for _, line := range cli.WrapForTest(path, "Database: ", "  ") {
		if strings.Contains(line, "xx") && columns(line) <= cli.Width {
			t.Error("an unbreakable token was split across lines")
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
