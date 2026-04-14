package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Mock struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
	RawBody []byte            `json:"-"`
	DelayMs int               `json:"delay_ms"`
	Enabled bool              `json:"enabled"`
	Source  string            `json:"source"`
}

type MockStore struct {
	mu    sync.RWMutex
	mocks []Mock
	dir   string
}

func NewMockStore(dir string) *MockStore {
	return &MockStore{dir: dir}
}

func (s *MockStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mocks, err := loadMocksFromDir(s.dir)
	if err != nil {
		return err
	}
	s.mocks = mocks
	return nil
}

func (s *MockStore) Find(method, path string) *Mock {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.mocks {
		if !s.mocks[i].Enabled {
			continue
		}
		if strings.EqualFold(s.mocks[i].Method, method) && matchPath(s.mocks[i].Path, path) {
			return &s.mocks[i]
		}
	}
	return nil
}

func (s *MockStore) All() []Mock {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Mock, len(s.mocks))
	copy(result, s.mocks)
	return result
}

func (s *MockStore) Toggle(index int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if index < 0 || index >= len(s.mocks) {
		return false
	}
	s.mocks[index].Enabled = !s.mocks[index].Enabled
	return true
}

func (s *MockStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.mocks)
}

func loadMocksFromDir(dir string) ([]Mock, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}

	var mocks []Mock
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", file, err)
		}

		var mock Mock
		if err := json.Unmarshal(data, &mock); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", file, err)
		}

		if mock.Method == "" || mock.Path == "" {
			return nil, fmt.Errorf("%s: method and path are required", file)
		}
		if mock.Status == 0 {
			mock.Status = 200
		}

		mock.RawBody = []byte(mock.Body)
		mock.Enabled = true
		mock.Source = filepath.Base(file)
		mocks = append(mocks, mock)
	}

	return mocks, nil
}

// matchPath supports exact matches and simple wildcards.
// Example: /api/v1/users/* matches /api/v1/users/123
func matchPath(pattern, path string) bool {
	if pattern == path {
		return true
	}

	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")

	if len(patternParts) != len(pathParts) {
		return false
	}

	for i := range patternParts {
		if patternParts[i] == "*" {
			continue
		}
		if patternParts[i] != pathParts[i] {
			return false
		}
	}

	return true
}
