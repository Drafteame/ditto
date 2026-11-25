package httpmock

import (
	"encoding/json"
	"net/http"
	"net/url"
	pathpkg "path"
	"reflect"
	"strings"
)

// matchPath supports exact matches and segment wildcards.
// Examples:
//   - /api/v1/users/* matches /api/v1/users/123
//   - /tickets-bff/tkt_* matches /tickets-bff/tkt_8WNAsd8wsRnf0xC0m
func matchPath(pattern, actualPath string) bool {
	if pattern == actualPath {
		return true
	}

	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(actualPath, "/")

	if len(patternParts) != len(pathParts) {
		return false
	}

	for i := range patternParts {
		ok, err := pathpkg.Match(patternParts[i], pathParts[i])
		if err != nil || !ok {
			return false
		}
	}

	return true
}

// pathSpecificity prefers exact paths over wildcard paths when the same
// request would match multiple mocks with equally specific match conditions.
func pathSpecificity(pattern string) int {
	return len(strings.ReplaceAll(pattern, "*", ""))
}

// matchConditions checks whether the request satisfies the given Match block.
// An empty Match always matches.
func matchConditions(m Match, r *http.Request, reqBody []byte) bool {
	if m.IsEmpty() {
		return true
	}

	if !matchQuery(m.Query, r.URL.Query()) {
		return false
	}
	if !matchHeaders(m.Headers, r.Header) {
		return false
	}
	if len(m.Body) > 0 && !matchBody(m.Body, reqBody) {
		return false
	}
	return true
}

func matchQuery(expected map[string]string, actual url.Values) bool {
	for k, v := range expected {
		if actual.Get(k) != v {
			return false
		}
	}
	return true
}

func matchHeaders(expected map[string]string, actual http.Header) bool {
	for k, v := range expected {
		if actual.Get(k) != v {
			return false
		}
	}
	return true
}

// matchBody returns true when every field in expected is present in actual
// with the same value (partial / subset match).
func matchBody(expected, actual []byte) bool {
	if len(actual) == 0 {
		return false
	}
	var exp, got any
	if err := json.Unmarshal(expected, &exp); err != nil {
		return false
	}
	if err := json.Unmarshal(actual, &got); err != nil {
		return false
	}
	return jsonSubset(exp, got)
}

// jsonSubset reports whether expected is a subset of actual.
// Maps are compared field-by-field; non-map values must be deeply equal.
func jsonSubset(expected, actual any) bool {
	expMap, expIsMap := expected.(map[string]any)
	gotMap, gotIsMap := actual.(map[string]any)

	if expIsMap && gotIsMap {
		for k, v := range expMap {
			gv, ok := gotMap[k]
			if !ok || !jsonSubset(v, gv) {
				return false
			}
		}
		return true
	}

	return reflect.DeepEqual(expected, actual)
}

func equalCaseInsensitive(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	lowerA := make(map[string]string, len(a))
	lowerB := make(map[string]string, len(b))
	for k, v := range a {
		lowerA[strings.ToLower(k)] = v
	}
	for k, v := range b {
		lowerB[strings.ToLower(k)] = v
	}
	return reflect.DeepEqual(lowerA, lowerB)
}

func jsonEqual(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
