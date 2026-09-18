package gpu

import (
	"strings"
	"testing"
)

func TestTailWriter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		max    int
		writes []string
		want   string
	}{
		{
			name:   "keeps everything under the limit",
			max:    16,
			writes: []string{"abc", "def"},
			want:   "abcdef",
		},
		{
			name:   "drops the head once writes exceed the limit",
			max:    5,
			writes: []string{"abc", "def", "ghi"},
			want:   "efghi",
		},
		{
			name:   "a single oversized write keeps only its tail",
			max:    4,
			writes: []string{"abcdefghij"},
			want:   "ghij",
		},
		{
			name:   "an oversized write discards everything buffered before it",
			max:    4,
			writes: []string{"xy", "abcdefghij"},
			want:   "ghij",
		},
		{
			name:   "multiple oversize writes discard everything other than final tail",
			max:    4,
			writes: []string{"abcdefghij", "klmnopqrst", "uvwxyz1234567890"},
			want:   "7890",
		},
		{
			name:   "empty writes",
			max:    4,
			writes: []string{},
			want:   "",
		},
		{
			name:   "exactly at the limit",
			max:    6,
			writes: []string{"abc", "def"},
			want:   "abcdef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := &tailWriter{max: tt.max}
			for _, s := range tt.writes {
				n, err := w.Write([]byte(s))
				if err != nil {
					t.Fatalf("Write(%q) returned error %v", s, err)
				}
				if n != len(s) {
					t.Errorf("Write(%q) = %d, want %d", s, n, len(s))
				}
			}

			if got := w.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
			if len(w.buf) > 2*tt.max {
				t.Errorf("retained %d bytes, want at most %d", len(w.buf), 2*tt.max)
			}
		})
	}
}

// The retained buffer must stay bounded no matter how much is written through it.
func TestTailWriterBoundsRetention(t *testing.T) {
	t.Parallel()

	const max = 1024
	w := &tailWriter{max: max}
	chunk := []byte(strings.Repeat("x", 4096))
	for range 256 {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("Write returned error %v", err)
		}
	}

	if len(w.buf) > 2*max {
		t.Errorf("after writing 1MB: retained %d bytes, want at most %d", len(w.buf), 2*max)
	}
	if got := w.String(); len(got) != max {
		t.Errorf("String() length = %d, want %d", len(got), max)
	}
}
