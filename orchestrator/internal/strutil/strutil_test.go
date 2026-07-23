package strutil

import "testing"

func TestDefault(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		fallback string
		want     string
	}{
		{"empty uses fallback", "", "fb", "fb"},
		{"whitespace uses fallback", "   ", "fb", "fb"},
		{"value trimmed", "  hello  ", "fb", "hello"},
		{"value kept", "hello", "fb", "hello"},
		{"empty fallback", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Default(tc.value, tc.fallback); got != tc.want {
				t.Fatalf("Default(%q, %q) = %q, want %q", tc.value, tc.fallback, got, tc.want)
			}
		})
	}
}
