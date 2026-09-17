package cli

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
)

// Width is the column budget human-readable output is laid out for.
//
// It is a fixed design width, not a measurement of the terminal. Output that is
// composed for 80 columns is readable in an 80-column terminal, in a wider one,
// and in a log file or a pasted transcript — none of which report a width.
// Detecting the terminal would make the same command produce different bytes in
// different places, which is worse for everyone reading the output later.
//
// It governs text this program composes. It does not govern JSON, the MCP
// payloads, or a verbatim document such as AGENTS.md, and a single raw value a
// source published may exceed it where truncating would destroy the value; each
// such surface says what it does.
const Width = 80

// displayWidth is how many columns a string occupies, under a deliberately
// small model: a combining mark adds nothing, a character from a range that
// terminals render double-wide counts two, and everything else counts one.
//
// This is not a full implementation of Unicode east-asian width, which would be
// a table larger than the rest of this file for content that is almost entirely
// ASCII. It is enough to keep wrapping and truncation honest when a model name
// or harness label is not, and to keep the width tests measuring what a reader
// sees rather than how many bytes it took.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
			// A combining mark attaches to the previous character.
		case isWide(r):
			w += 2
		default:
			w++
		}
	}
	return w
}

// isWide reports whether a rune is normally rendered two columns wide.
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK radicals, Kangxi, CJK symbols
		r >= 0x3041 && r <= 0x33FF, // Hiragana, Katakana, Hangul, CJK compatibility
		r >= 0x3400 && r <= 0x4DBF, // CJK extension A
		r >= 0x4E00 && r <= 0x9FFF, // CJK unified ideographs
		r >= 0xA000 && r <= 0xA4CF, // Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // Fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, // Pictographs and emoticons
		r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD: // CJK extensions B and beyond
		return true
	}
	return false
}

// wrap breaks text into lines that fit within Width columns, prefixing the
// first line with first and every later line with rest.
//
// Words are never split: a single word longer than the budget takes a line of
// its own and overflows, because a model name or a URL is more useful whole
// than cut in half. Callers that must not overflow truncate first.
//
// A blank line in the input is preserved, so a caller can wrap a paragraph
// without losing its shape.
func wrap(text, first, rest string) []string {
	var out []string
	for i, para := range strings.Split(text, "\n") {
		prefix := rest
		if i == 0 {
			prefix = first
		}
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapParagraph(para, prefix, rest)...)
	}
	return out
}

func wrapParagraph(text, first, rest string) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{strings.TrimRight(first, " ")}
	}
	var (
		out    []string
		line   = first
		filled = false
	)
	for _, word := range words {
		switch {
		case !filled:
			// A value that does not fit beside its label, but would fit on a
			// line of its own, starts on the next line rather than pushing the
			// first one over. A long label and a long path are each reasonable;
			// the two together are what overflows.
			if displayWidth(first)+displayWidth(word) > Width &&
				displayWidth(rest)+displayWidth(word) <= Width {
				out = append(out, strings.TrimRight(first, " "))
				line = rest + word
			} else {
				line += word
			}
			filled = true
		case displayWidth(line)+1+displayWidth(word) <= Width:
			line += " " + word
		default:
			out = append(out, line)
			line = rest + word
		}
	}
	return append(out, line)
}

// truncate shortens s to at most max columns, marking that it did with an
// ellipsis. It cuts on a character boundary and never in the middle of a
// combining sequence, so the result is always valid UTF-8 and always renders.
//
// Truncation loses information, so it belongs only where the full value is
// reachable another way. Each caller says where that is.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if displayWidth(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	budget := max - 1 // room for the ellipsis
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := displayWidth(string(r))
		if w+rw > budget {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

// pad right-pads s to width columns, measuring what a reader sees rather than
// how many bytes it took, so a column of values with non-ASCII in it still
// lines up.
func pad(s string, width int) string {
	if n := width - displayWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// TruncateForTest and WrapForTest expose the two layout primitives to the
// width tests, which live in the external test package so that they exercise
// the command line the way a caller does.
func TruncateForTest(s string, max int) string      { return truncate(s, max) }
func WrapForTest(text, first, rest string) []string { return wrap(text, first, rest) }

// The query table's column budget.
//
// The rank column holds a rank and the two spaces that separate it from the
// label. The three floors are the narrowest each column can be and still say
// anything: a label that is still recognisable, a uid short enough to be worth
// copying, a number with an evidence flag on it. queryValueWidth is what a
// value column gets when there is room for it.
const (
	queryRankColumn = 6
	queryLabelFloor = 12
	queryUIDFloor   = 12
	queryValueFloor = 8
	queryValueWidth = 12
)

// queryTableFits reports whether a query table with metricColumns value columns
// can be laid out inside Width with every column at least at its floor.
//
// This is the rule that chooses between the table and the stacked layout, and
// it is arithmetic over the floors above rather than a remembered number of
// columns: move a floor and the layout gives way at the right place without
// anything else being edited.
func queryTableFits(metricColumns int) bool {
	return queryRankColumn+queryLabelFloor+queryUIDFloor+
		metricColumns*queryValueFloor <= Width
}

// queryBudget divides Width between the query table's columns, given how many
// metric columns there are and how wide the widest uid in the results is. It
// assumes queryTableFits already said yes.
//
// The uid is protected ahead of the label. It is the value a caller copies into
// `devmodels model <uid>`, so a truncated one costs them a step, while a
// truncated label is still perfectly recognisable beside it. The uid column is
// therefore sized to the widest uid actually present rather than to a guess,
// and only gives way when the label has already shrunk to its floor.
func queryBudget(metricColumns, widestUID int) (rank, label, uid, value int) {
	rank = queryRankColumn
	value = queryValueWidth
	if metricColumns > 0 {
		if v := (Width - rank - queryLabelFloor - queryUIDFloor) / metricColumns; v < value {
			value = v
		}
		if value < queryValueFloor {
			value = queryValueFloor
		}
	}

	remaining := Width - rank - metricColumns*value
	// One column of separation, and never wider than it needs to be.
	uid = widestUID + 1
	if uid > remaining-queryLabelFloor {
		uid = remaining - queryLabelFloor
	}
	if uid < queryUIDFloor {
		uid = queryUIDFloor
	}
	label = remaining - uid
	if label < queryLabelFloor {
		label = queryLabelFloor
	}
	return rank, label, uid, value
}

// metricFacts is the one-line "what kind of number is this" description shown
// under a metric key: value kind, unit, and which direction is better.
func metricFacts(m query.MetricDescription) string {
	parts := []string{string(m.ValueKind)}
	if m.Unit != "" {
		parts = append(parts, m.Unit)
	}
	switch m.Direction {
	case metrics.HigherIsBetter:
		parts = append(parts, "higher is better")
	case metrics.LowerIsBetter:
		parts = append(parts, "lower is better")
	}
	return strings.Join(parts, ", ")
}

// metricCoverage is the one-line "how much of this dataset has it" description:
// how many models carry a value, which sources supply it, and — in the full
// projection — how many individual values are stored.
func metricCoverage(m query.MetricDescription, catalogModels int) string {
	coverage := strconv.Itoa(m.CatalogModelsWithValue)
	if catalogModels > 0 {
		coverage += " of " + strconv.Itoa(catalogModels)
	}
	coverage += " models"
	parts := []string{coverage}
	if m.DataPoints != nil {
		parts = append(parts, strconv.Itoa(*m.DataPoints)+" data points")
	}
	if len(m.Sources) > 0 {
		parts = append(parts, strings.Join(m.Sources, ", "))
	}
	return strings.Join(parts, " · ")
}
