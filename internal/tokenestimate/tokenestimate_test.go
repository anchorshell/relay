package tokenestimate

import "testing"

func TestCountUsesTokenxStyleSegments(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int64
	}{
		{name: "empty", text: "", want: 0},
		{name: "whitespace", text: " \n\t", want: 0},
		{name: "english with punctuation", text: "Hello, world!", want: 4},
		{name: "numeric", text: "123456", want: 1},
		{name: "cjk rune count", text: "\u4f60\u597d\u4e16\u754c", want: 4},
		{name: "punctuation run", text: "....", want: 2},
		{name: "accented language rule", text: "cr\u00e8me br\u00fbl\u00e9e jalape\u00f1o", want: 7},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Count(tc.text); got != tc.want {
				t.Fatalf("Count(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

func TestCountByteLengthFallback(t *testing.T) {
	if got := CountByteLength(0); got != 0 {
		t.Fatalf("expected zero-length fallback to be 0, got %d", got)
	}
	if got := CountByteLength(1); got != 1 {
		t.Fatalf("expected nonzero fallback minimum to be 1, got %d", got)
	}
	if got := CountByteLength(13); got != 3 {
		t.Fatalf("expected byte fallback to ceil by default chars/token, got %d", got)
	}
}
