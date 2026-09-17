package epoch_test

import (
	"archive/zip"
	"bytes"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/epoch"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/testfixtures"
)

func TestMalformedRowsAreRecordedAndParsingContinues(t *testing.T) {
	files := testfixtures.DefaultEpochFiles()
	files["swe_bench_verified.csv"] = "Model version,mean_score\nmodel-a,0.5\nmodel\"b,0.5\nmodel-c,NaN\nmodel-d,0.6\n"
	res, err := epoch.Parse(testfixtures.EpochArchive(files))
	if err != nil {
		t.Fatal(err)
	}
	if find(res, "swe_bench_verified", "model-a", epoch.OwnEvaluationContext) == nil || find(res, "swe_bench_verified", "model-d", epoch.OwnEvaluationContext) == nil {
		t.Fatal("rows around a malformed row must still be read")
	}
	if find(res, "swe_bench_verified", "model-c", epoch.OwnEvaluationContext) != nil {
		t.Fatal("a NaN score must not be recorded")
	}
	var refs []string
	for _, r := range res.Rejected {
		refs = append(refs, r.SourceRowRef)
	}
	if !slices.Contains(refs, "swe_bench_verified.csv:3") || !slices.Contains(refs, "swe_bench_verified.csv:4") {
		t.Fatalf("the malformed row and the NaN row must be rejected with their line numbers: %v", refs)
	}
}

// A rejection reason quotes the cell it could not read, shortened when it is
// long. The reason is stored and returned as JSON, so shortening it must not
// cut a character in half.
func TestALongUnreadableScoreIsShortenedOnACharacterBoundary(t *testing.T) {
	files := testfixtures.DefaultEpochFiles()
	// "a" then two-byte runes puts a continuation byte exactly where a
	// byte-wise cut would land.
	score := "a" + strings.Repeat("é", 60)
	files["swe_bench_verified.csv"] = "Model version,mean_score\nmodel-a," + score + "\nmodel-b,0.5\n"

	res, err := epoch.Parse(testfixtures.EpochArchive(files))
	if err != nil {
		t.Fatal(err)
	}
	reason := ""
	for _, r := range res.Rejected {
		if r.SourceRowRef == "swe_bench_verified.csv:2" {
			reason = r.Reason
		}
	}
	if reason == "" {
		t.Fatalf("the unreadable score must be rejected with its line number: %+v", res.Rejected)
	}
	if !utf8.ValidString(reason) {
		t.Errorf("the reason is not valid UTF-8: %q", reason)
	}
	if !strings.Contains(reason, "…") {
		t.Errorf("a long cell must be shortened, got %q", reason)
	}
}

// A member whose data fails its checksum makes every read return the same
// error; that must fail the source rather than loop.
func TestCorruptArchiveMemberFailsTheSource(t *testing.T) {
	files := testfixtures.DefaultEpochFiles()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		fw, err := w.CreateHeader(&zip.FileHeader{Name: "data/" + name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(buf.Bytes(), []byte("0.7266"), []byte("0.7267"), 1)
	if bytes.Equal(corrupt, buf.Bytes()) {
		t.Fatal("fixture changed: the corrupted value is not present")
	}
	_, err := epoch.Parse(corrupt)
	if err == nil || !strings.Contains(err.Error(), "deepswe_external.csv") {
		t.Fatalf("a corrupt member must fail the source, got %v", err)
	}
}
