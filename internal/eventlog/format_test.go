package eventlog

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"ditto/internal/golden"
)

// TestLogEventJSONCharacterization pins the wire shape of Event (key names,
// order and omitempty behaviour) that the dashboard and --log-format json rely on.
func TestLogEventJSONCharacterization(t *testing.T) {
	var full Event
	fillSample(reflect.ValueOf(&full).Elem(), "")

	var out bytes.Buffer
	for _, event := range []Event{{}, full} {
		data, err := json.MarshalIndent(event, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		out.Write(data)
		out.WriteByte('\n')
	}
	golden.Assert(t, "log_event.golden", out.Bytes())
}

// TestRequestLogFormatCharacterization pins both --log-format outputs.
func TestRequestLogFormatCharacterization(t *testing.T) {
	event := Event{
		ID: "log-1", Timestamp: "2026-01-02T03:04:05Z", Type: "MOCK", Method: "GET",
		Path: "/api/users?x=1", Status: 200, DurationMs: 12, MockIndex: 3,
		RequestHeaders: map[string][]string{"Accept": {"application/json"}},
		ResponseBody:   `{"html":"<b>&</b>"}`,
	}
	var out bytes.Buffer
	NewPrinter(&out, false).Log(event)
	NewPrinter(&out, true).Log(event)
	golden.Assert(t, "request_log.golden", out.Bytes())
}

// fillSample sets every field reachable from v to a deterministic non-zero
// value derived from its field path, so omitempty fields all show up.
func fillSample(v reflect.Value, name string) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fillSample(v.Field(i), v.Type().Field(i).Name)
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillSample(v.Elem(), name)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillSample(v.Index(0), name)
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		val := reflect.New(v.Type().Elem()).Elem()
		fillSample(val, name)
		v.SetMapIndex(reflect.ValueOf(name+"-key"), val)
	case reflect.String:
		v.SetString(name)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(int64(len(name)))
	default:
		panic("fillSample: unsupported kind " + v.Kind().String())
	}
}
