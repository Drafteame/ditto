// Package golden compares test output with files under testdata/ and
// rewrites them when tests run with -update.
package golden

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden files")

// Assert compares got with testdata/name, rewriting the file when -update
// is set.
func Assert(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden (run with -update to create): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s mismatch; run `go test -run %s -update` if the change is intended\n--- got ---\n%s", path, t.Name(), got)
	}
}
