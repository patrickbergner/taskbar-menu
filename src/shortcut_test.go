//go:build windows

package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

// makeTestLink writes a .lnk at path pointing at target, optionally with an
// explicit icon location. Deliberately not writeShortcut: that one always aims
// at TaskbarMenu.exe with --launch arguments, and what these tests need is a
// plain shortcut of the shape a Start Menu is full of.
func makeTestLink(t *testing.T, path, target, icon string) {
	t.Helper()
	link := coCreateInstance(&clsidShellLink, &iidShellLinkW)
	if link == nil {
		t.Skip("ShellLink unavailable")
	}
	defer release(link)

	setLinkString(link, shlSetPath, target)
	if icon != "" {
		p := utf16Ptr(icon)
		comCall(link, shlSetIconLocation, uintptr(unsafe.Pointer(p)), 0)
		runtime.KeepAlive(p)
	}

	pf := comQI(link, &iidPersistFile)
	if pf == nil {
		t.Fatal("IPersistFile unavailable")
	}
	defer release(pf)

	p := utf16Ptr(path)
	hr := comCall(pf, persistFileSave, uintptr(unsafe.Pointer(p)), 1)
	runtime.KeepAlive(p)
	if hr != 0 {
		t.Fatalf("saving %s: hr=%#x", path, hr)
	}
}

// The regression this file exists for. A Start Menu is mostly shortcuts that
// store no icon location of their own -- Firefox, Thunderbird, VS Code -- and
// SHGetFileInfo(SHGFI_ICONLOCATION) reports nothing for them. They used to fall
// through to the .lnk itself, which holds no icon resources, and every one of
// them drew the generic application icon.
func TestResolveIconSourceFollowsShortcutToTarget(t *testing.T) {
	comSetup(t)
	defer purgeIcons()

	target := expandEnv(`%SystemRoot%\System32\notepad.exe`)
	link := filepath.Join(t.TempDir(), "Notepad.lnk")
	makeTestLink(t, link, target, "")

	file, idx := resolveIconSource(link, "")
	if !samePath(file, target) {
		t.Errorf("resolveIconSource(%q) = (%q, %d), want the target %q", link, file, idx, target)
	}
	if h := iconFor(file, idx, 16); h == genericIcon() {
		t.Error("a shortcut to notepad.exe still draws the generic icon")
	}
}

// A shortcut that does store an icon location is answered from it rather than
// from its target: that is how Excel.lnk draws xlicons.exe instead of the
// EXCEL.EXE it actually runs.
func TestResolveIconSourceUsesShortcutIconLocation(t *testing.T) {
	comSetup(t)
	defer purgeIcons()

	icon := expandEnv(`%SystemRoot%\System32\imageres.dll`)
	link := filepath.Join(t.TempDir(), "Custom.lnk")
	makeTestLink(t, link, expandEnv(`%SystemRoot%\System32\notepad.exe`), icon)

	file, _ := resolveIconSource(link, "")
	if !samePath(file, icon) {
		t.Errorf("resolveIconSource(%q) = %q, want the stored icon location %q", link, file, icon)
	}
}

// An explicit config icon still outranks everything the shortcut says.
func TestResolveIconSourceExplicitIconBeatsShortcut(t *testing.T) {
	comSetup(t)
	defer purgeIcons()

	link := filepath.Join(t.TempDir(), "Notepad.lnk")
	makeTestLink(t, link, expandEnv(`%SystemRoot%\System32\notepad.exe`), "")

	file, idx := resolveIconSource(link, `C:\Windows\System32\shell32.dll,42`)
	if file != `C:\Windows\System32\shell32.dll` || idx != 42 {
		t.Errorf("resolveIconSource = (%q, %d), want the explicit spec", file, idx)
	}
}

// Why maxLinkHops guards a shape that is rare rather than one that is common:
// the shell collapses a chain as it writes it. Pointing a shortcut at another
// shortcut stores the second one's *target*, not the second one, so a link
// written through IShellLink can never start a chain -- which is also why a
// cycle cannot be built here to test the bound against. The bound stays for the
// .lnk the shell did not write.
//
// Built in this order because the shell also refuses to save a link whose target
// does not exist yet: B has to point somewhere real before A can aim at it.
func TestShortcutChainsAreCollapsedByTheShell(t *testing.T) {
	comSetup(t)
	defer purgeIcons()

	target := expandEnv(`%SystemRoot%\System32\notepad.exe`)
	dir := t.TempDir()
	a := filepath.Join(dir, "A.lnk")
	b := filepath.Join(dir, "B.lnk")
	makeTestLink(t, b, target, "")
	makeTestLink(t, a, b, "")

	if _, _, got := linkIconSource(a); !samePath(got, target) {
		t.Errorf("a link onto a link resolved to %q, want the collapsed target %q", got, target)
	}
	if file, _ := resolveIconSource(a, ""); !samePath(file, target) {
		t.Errorf("resolveIconSource(%q) = %q, want %q", a, file, target)
	}
}

func TestIsLinkFile(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`C:\Users\x\Start Menu\Firefox.lnk`, true},
		{`C:\Users\x\Start Menu\Firefox.LNK`, true},
		{`C:\Windows\notepad.exe`, false},
		{`C:\Users\x\Bookmark.url`, false}, // resolved through its association
		{`shell:AppsFolder\X_y!App`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := isLinkFile(c.in); got != c.want {
			t.Errorf("isLinkFile(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

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
