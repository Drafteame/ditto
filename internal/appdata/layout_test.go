package appdata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDataLayoutCreatesBundleCompatibleDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ditto")

	layout, err := Ensure(root)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	wantDirs := []string{
		layout.Root,
		layout.MocksDir,
		layout.DescriptorsDir,
		layout.EventTemplatesDir,
		layout.SequencesDir,
		layout.AdapterProfilesDir,
		layout.RecordingsDir,
		layout.ScenariosDir,
	}
	for _, dir := range wantDirs {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("expected %s to exist: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", dir)
		}
	}

	if got, want := layout.ConfigPath, filepath.Join(root, "config.json"); got != want {
		t.Fatalf("ConfigPath = %q, want %q", got, want)
	}
}

func TestSeedExampleMockOnlyIntoAnEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := SeedExampleMock(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "example.json")); err != nil {
		t.Fatalf("example not seeded into an empty dir: %v", err)
	}

	// Once the user has mocks of their own, deleting the example is final.
	if err := os.Rename(filepath.Join(dir, "example.json"), filepath.Join(dir, "mine.json")); err != nil {
		t.Fatal(err)
	}
	if err := SeedExampleMock(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "example.json")); !os.IsNotExist(err) {
		t.Fatalf("example re-seeded next to user mocks: %v", err)
	}
}
