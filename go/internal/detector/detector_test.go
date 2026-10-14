package detector

import (
	"strings"
	"testing"
)

// Tests use ordinary text and invalid/empty documents. No credential or command
// samples are created, and detection never executes the text it inspects.
func TestEmbeddedConfigurationAndOrdinaryDocuments(t *testing.T) {
	d, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.rules) != 23 {
		t.Fatalf("enabled rules=%d, want 23", len(d.rules))
	}
	for _, document := range []string{`[]`, `[{"name":"普通说明","count":2,"enabled":true}]`, `[{"key":null,"value":"ordinary text"},{"key":[],"value":null}]`} {
		got, err := d.Scan(strings.NewReader(document), "results.json")
		if err != nil || len(got) != 0 {
			t.Fatalf("ordinary document: findings=%v, error=%v", got, err)
		}
	}
}

func TestInvalidDocuments(t *testing.T) {
	d, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"", "{", "[] {}", "[] garbage"} {
		if _, err := d.Scan(strings.NewReader(input), "results.json"); err == nil {
			t.Errorf("accepted invalid JSON %q", input)
		}
	}
	if _, err := d.ScanFile("missing.json"); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestNormalizationAndBase64(t *testing.T) {
	for _, value := range []string{"", "null", "${NAME}", "{{name}}", "<name>", "/tmp/file"} {
		if isStatic("name", value) {
			t.Errorf("placeholder %q accepted", value)
		}
	}
	if !isStatic("name", "ordinary text") {
		t.Fatal("ordinary static value rejected")
	}
	for _, value := range []string{"aGVsbG8=", "a G V s b G 8=", "aGVsbG8=ignored"} {
		got, err := decodeBase64(value)
		if err != nil || string(got) != "hello" {
			t.Errorf("base64 %q: %q, error=%v", value, got, err)
		}
	}
	if _, err := decodeBase64("aGVsbG8"); err == nil {
		t.Fatal("missing padding accepted")
	}
}

func TestSimilarity(t *testing.T) {
	for _, tc := range []struct {
		first, second string
		want          float64
	}{{"", "", 0}, {"ordinary text", "ORDINARY_TEXT", 1}, {"ab", "xy", 0}, {"MARTHA", "MARHTA", 0.9611111111111111}} {
		if got := similarity(tc.first, tc.second); got != tc.want {
			t.Errorf("similarity(%q,%q)=%v, want %v", tc.first, tc.second, got, tc.want)
		}
	}
}
