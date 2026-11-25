package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Helpers shared by the stores that keep one JSON file per entity in a
// directory (templates, sequences, recordings, channel modes, profiles).

// writeFileAtomic writes data to a temporary file next to path, syncs it and
// renames it over path, so readers never see a partial file.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// writeJSONFile writes v as indented JSON with a trailing newline. Unless
// overwrite is set, it fails with os.ErrExist when path already exists.
func writeJSONFile(path string, v any, overwrite bool) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return os.ErrExist
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

// slug lowercases s and collapses every run of characters that are not
// letters or digits into a single dash, trimming dashes at both ends.
func slug(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// validID reports whether id is already a slug, which also makes it safe to
// use as a file name inside a store directory.
func validID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" &&
		id != "." &&
		id != ".." &&
		!strings.Contains(id, "/") &&
		!strings.Contains(id, string(os.PathSeparator)) &&
		id == slug(id)
}

// hashedID returns the slug of name (at most 48 runes, or fallback when
// empty) followed by the first 8 hex digits of sha256(hashInput).
func hashedID(name, fallback, hashInput string) string {
	base := slug(name)
	if base == "" {
		base = fallback
	}
	if runes := []rune(base); len(runes) > 48 {
		base = strings.Trim(string(runes[:48]), "-")
	}
	sum := sha256.Sum256([]byte(hashInput))
	return fmt.Sprintf("%s-%s", base, hex.EncodeToString(sum[:])[:8])
}

// nameAttempt is the hash input for the attempt-th ID derived from name;
// attempts after the first resolve collisions.
func nameAttempt(name string, attempt int) string {
	if attempt > 0 {
		return fmt.Sprintf("%s\n%d", name, attempt)
	}
	return name
}
