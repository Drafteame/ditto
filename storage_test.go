package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDataLayoutCreatesBundleCompatibleDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ditto")

	layout, err := EnsureDataLayout(root)
	if err != nil {
		t.Fatalf("EnsureDataLayout() error = %v", err)
	}

	wantDirs := []string{
		layout.Root,
		layout.MocksDir,
		layout.DescriptorsDir,
		layout.EventTemplatesDir,
		layout.SequencesDir,
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
