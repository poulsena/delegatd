package domain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeManualInputCanonicalizesBoundariesWithoutChangingContent(t *testing.T) {
	input := []byte("\r\n  first line  \rsecond line\r\n\t\r\n")
	got, err := NormalizeManualInput(input)
	if err != nil {
		t.Fatalf("NormalizeManualInput() error = %v", err)
	}
	if got.Version != 1 || got.Source != TaskSourceManual {
		t.Fatalf("input envelope = %#v", got)
	}
	if got.Instructions != "  first line  \nsecond line" {
		t.Fatalf("instructions = %q", got.Instructions)
	}
}

func TestNormalizeManualInputRejectsInvalidAndOversizedInput(t *testing.T) {
	for name, input := range map[string][]byte{
		"empty":        []byte(" \r\n\t"),
		"nul":          []byte("valid\x00input"),
		"invalid utf8": []byte{0xff, 0xfe},
		"oversized":    []byte(strings.Repeat("x", maxManualInputSize+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeManualInput(input); err == nil {
				t.Fatal("NormalizeManualInput() error = nil")
			}
		})
	}
}

func TestSnapshotCanonicalizesAndReturnsCopies(t *testing.T) {
	first, err := NewSnapshot(map[string]any{"b": "<value>", "a": json.Number("1")})
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	second, err := ParseSnapshot([]byte(`{"b":"<value>","a":1}`))
	if err != nil {
		t.Fatalf("ParseSnapshot() error = %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("canonical bytes differ: %s versus %s", first.Bytes(), second.Bytes())
	}
	copyOfBytes := first.Bytes()
	copyOfBytes[0] = '['
	if bytes.Equal(copyOfBytes, first.Bytes()) {
		t.Fatal("Bytes() returned an aliased buffer")
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		Value Snapshot `json:"value"`
	}{Value: first}); err != nil {
		t.Fatalf("json.Encode() error = %v", err)
	}
	encoded := bytes.TrimSpace(buffer.Bytes())
	if string(encoded) != `{"value":{"a":1,"b":"<value>"}}` {
		t.Fatalf("encoded = %s", encoded)
	}
}

func TestParseTaskIDRequiresBoundedRFC4648Base32(t *testing.T) {
	valid := TaskID("task_" + strings.Repeat("A", 26))
	if got, err := ParseTaskID(string(valid)); err != nil || got != valid {
		t.Fatalf("ParseTaskID(valid) = %q, %v", got, err)
	}
	for _, value := range []string{"", "task_short", "task_" + strings.Repeat("a", 26), "task_" + strings.Repeat("A", 129)} {
		if _, err := ParseTaskID(value); err == nil {
			t.Fatalf("ParseTaskID(%q) error = nil", value)
		}
	}
}

func TestSnapshotRejectsMalformedValuesAndEmptyMarshal(t *testing.T) {
	if _, err := NewSnapshot(func() {}); err == nil {
		t.Fatal("NewSnapshot(function) error = nil")
	}
	for name, input := range map[string][]byte{
		"empty":    nil,
		"invalid":  []byte("{"),
		"multiple": []byte(`{"a":1} {"b":2}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSnapshot(input); err == nil {
				t.Fatal("ParseSnapshot() error = nil")
			}
		})
	}
	var empty Snapshot
	if _, err := empty.MarshalJSON(); err == nil {
		t.Fatal("MarshalJSON(empty) error = nil")
	}
}
