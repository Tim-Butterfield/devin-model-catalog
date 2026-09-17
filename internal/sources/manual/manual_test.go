package manual_test

import (
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/manual"
)

const document = `{
  "source": {"code": "manual_windows", "name": "Windows", "access_basis": "typed in"},
  "observations": [{"metric": "context_window_tokens", "model": "A", "evaluation_context": "vendor", "value": 1}]
}`

func TestParseAcceptsExactlyOneDocument(t *testing.T) {
	if _, err := manual.Parse([]byte(document)); err != nil {
		t.Fatalf("a valid document: %v", err)
	}
	for _, trailing := range []string{"{}", `{"source": {}}`, "garbage"} {
		if _, err := manual.Parse([]byte(document + "\n" + trailing)); err == nil || !strings.Contains(err.Error(), "exactly one JSON object") {
			t.Errorf("trailing %q must be rejected, got %v", trailing, err)
		}
	}
}
