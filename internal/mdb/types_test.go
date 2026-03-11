package mdb

import (
	"testing"
	"time"
)

func TestNormalizeValue_Boolean(t *testing.T) {
	// Access Yes/No fields arrive as int16: -1 = true, 0 = false.
	tests := []struct {
		name  string
		input interface{}
		want  interface{}
	}{
		{"int16 true (-1)", int16(-1), true},
		{"int16 false (0)", int16(0), false},
		{"bool true", true, true},
		{"bool false", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeValue(nil, tt.input)
			if got != tt.want {
				t.Errorf("NormalizeValue(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeValue_Integers(t *testing.T) {
	tests := []struct {
		input interface{}
		want  interface{}
	}{
		{int32(42), int64(42)},
		{int32(-1), int64(-1)},
		{int64(100), int64(100)},
	}
	for _, tt := range tests {
		got := NormalizeValue(nil, tt.input)
		if got != tt.want {
			t.Errorf("NormalizeValue(%v) = %v (%T), want %v (%T)",
				tt.input, got, got, tt.want, tt.want)
		}
	}
}

func TestNormalizeValue_Float(t *testing.T) {
	got := NormalizeValue(nil, float32(3.14))
	if _, ok := got.(float64); !ok {
		t.Errorf("expected float64, got %T", got)
	}
}

func TestNormalizeValue_DateTime(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	ts := time.Date(2024, 6, 15, 12, 0, 0, 0, loc)
	got := NormalizeValue(nil, ts)
	want := "2024-06-15T16:00:00Z" // UTC
	if got != want {
		t.Errorf("NormalizeValue(time) = %q, want %q", got, want)
	}
}

func TestNormalizeValue_ZeroTime(t *testing.T) {
	got := NormalizeValue(nil, time.Time{})
	if got != nil {
		t.Errorf("zero time should normalize to nil, got %v", got)
	}
}

func TestNormalizeValue_Binary(t *testing.T) {
	got := NormalizeValue(nil, []byte{0x48, 0x65, 0x6c, 0x6c, 0x6f})
	// "Hello" in base64
	if got != "SGVsbG8=" {
		t.Errorf("NormalizeValue([]byte) = %v, want base64 SGVsbG8=", got)
	}
}

func TestNormalizeValue_EmptyBinary(t *testing.T) {
	got := NormalizeValue(nil, []byte{})
	if got != nil {
		t.Errorf("empty []byte should normalize to nil, got %v", got)
	}
}

func TestNormalizeValue_Nil(t *testing.T) {
	got := NormalizeValue(nil, nil)
	if got != nil {
		t.Errorf("nil should stay nil, got %v", got)
	}
}

func TestNormalizeValue_String(t *testing.T) {
	got := NormalizeValue(nil, "hello world")
	if got != "hello world" {
		t.Errorf("string passthrough failed: %v", got)
	}
}

func TestStripHyperlinkMeta(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com", "https://example.com"},
		{"Click here#https://example.com#", "https://example.com"},
		{"#https://example.com#tip", "https://example.com"},
		{"display#addr#screen", "addr"},
	}
	for _, tt := range tests {
		got := stripHyperlinkMeta(tt.input)
		if got != tt.want {
			t.Errorf("stripHyperlinkMeta(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
