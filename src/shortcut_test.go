//go:build windows

package main

import (
	"strings"
	"testing"
)

// The whole point of deriving the AUMID from the id alone: regenerating a
// shortcut must produce the identical AUMID so an existing taskbar pin is never
// orphaned. This locks that guarantee in.
func TestAumidStableAcrossRegenerations(t *testing.T) {
	if a, b := aumid("firefox-work"), aumid("firefox-work"); a != b {
		t.Fatalf("aumid not stable: %q vs %q", a, b)
	}
	// The value must depend on nothing but the id -- label, path, args and icon
	// can all change between runs, and none of them are inputs here.
	if aumid("firefox") == aumid("firefox-work") {
		t.Error("different ids must yield different AUMIDs")
	}
}

func TestAumidFormat(t *testing.T) {
	cases := []struct{ id, want string }{
		{"firefox", "TaskbarMenu.Launcher.firefox"},
		{"firefox-work", "TaskbarMenu.Launcher.firefox_work"},
		{"a.b c", "TaskbarMenu.Launcher.a_b_c"},
		{"Straße!", "TaskbarMenu.Launcher.Stra_e_"}, // ranged by rune: ß -> one '_'
	}
	for _, c := range cases {
		if got := aumid(c.id); got != c.want {
			t.Errorf("aumid(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

// AUMID sections are dot-separated and each must stay <=64 chars; the prefix
// already holds two, so the token is capped. A long id truncates and gets a
// stable hash suffix so two long ids that share a prefix still differ -- and the
// suffix must itself be reproducible.
func TestAumidLongIDTruncatesStablyAndUniquely(t *testing.T) {
	long1 := strings.Repeat("a", 100) + "ONE"
	long2 := strings.Repeat("a", 100) + "TWO"

	a1, a2 := aumid(long1), aumid(long2)

	// Every dot-separated section within the format limit.
	for _, sec := range strings.Split(a1, ".") {
		if len(sec) > 64 {
			t.Errorf("section %q exceeds 64 chars", sec)
		}
	}
	if len(a1) > 128 {
		t.Errorf("aumid too long: %d", len(a1))
	}
	// Reproducible.
	if aumid(long1) != a1 {
		t.Error("long-id aumid not reproducible")
	}
	// Distinct despite the shared 100-char prefix.
	if a1 == a2 {
		t.Error("long ids sharing a prefix collided")
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"firefox", "firefox"},
		{`a/b\c:d`, "a_b_c_d"},
		{`x<>|?*"y`, "x______y"}, // 6 forbidden chars -> 6 underscores
		{"   ", "launcher"},
		{"", "launcher"},
	}
	for _, c := range cases {
		if got := sanitizeFileName(c.in); got != c.want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
