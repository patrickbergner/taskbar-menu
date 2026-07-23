//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseIconSpec(t *testing.T) {
	cases := []struct {
		in      string
		wantF   string
		wantIdx int32
	}{
		{`C:\icons\app.ico`, `C:\icons\app.ico`, 0},
		{`C:\Windows\System32\imageres.dll,109`, `C:\Windows\System32\imageres.dll`, 109},
		{`shell32.dll,-16`, `shell32.dll`, -16}, // negative index = resource id
		{`C:\a,b\c.dll,3`, `C:\a,b\c.dll`, 3},   // commas in the path are fine
		{`C:\a,b\c.ico`, `C:\a,b\c.ico`, 0},     // trailing part is not a number
		{`  C:\x.exe , 2 `, `C:\x.exe`, 2},      // tolerate stray spaces
		{``, ``, 0},
		{`,5`, `,5`, 0}, // no filename before the comma: not an index
	}
	for _, c := range cases {
		f, idx := parseIconSpec(c.in)
		if f != c.wantF || idx != c.wantIdx {
			t.Errorf("parseIconSpec(%q) = (%q, %d), want (%q, %d)", c.in, f, idx, c.wantF, c.wantIdx)
		}
	}
}

// A drive letter must never be mistaken for a URL scheme, or every absolute
// path would be routed through AssocQueryString instead of the shell.
func TestURLScheme(t *testing.T) {
	cases := []struct{ in, want string }{
		{`https://example.com/`, "https"},
		{`HTTP://Example.com/`, "http"},
		{`mailto:someone@example.com`, "mailto"},
		{`ms-settings:display`, "ms-settings"},
		{`C:\Windows\notepad.exe`, ""},
		{`C:/Windows/notepad.exe`, ""},
		{`notepad.exe`, ""},
		{`\\server\share\file.txt`, ""},
		{`file with: colon.txt`, ""}, // space is not valid in a scheme
		{``, ""},
	}
	for _, c := range cases {
		if got := urlScheme(c.in); got != c.want {
			t.Errorf("urlScheme(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveIconSourcePrefersExplicitSpec(t *testing.T) {
	file, idx := resolveIconSource(`C:\anything.exe`, `C:\Windows\System32\imageres.dll,42`)
	if file != `C:\Windows\System32\imageres.dll` || idx != 42 {
		t.Errorf("explicit icon spec ignored: got (%q, %d)", file, idx)
	}
}

func TestResolveIconSourceEmpty(t *testing.T) {
	if file, idx := resolveIconSource("", ""); file != "" || idx != 0 {
		t.Errorf("got (%q, %d), want empty", file, idx)
	}
}

// A real multi-icon system DLL: this is the case the "icons from exe files,
// multiple if there are more than 1" requirement is about.
func TestIconCountAndExtractionFromSystemDLL(t *testing.T) {
	const dll = `C:\Windows\System32\imageres.dll`

	n := iconCount(dll)
	if n < 2 {
		t.Fatalf("iconCount(%s) = %d, want many", dll, n)
	}
	t.Logf("%s holds %d icons", dll, n)

	// Extraction must work at each DPI's pixel size, not just at 16.
	for _, size := range []int32{16, 20, 24, 32, 48} {
		h := extractIcon(dll, 0, size)
		if h == 0 {
			t.Errorf("extractIcon(index 0, size %d) failed", size)
			continue
		}
		destroyIcon(h)
	}

	// A high index inside the file must resolve to a different icon than 0.
	if h := extractIcon(dll, int32(n-1), 32); h == 0 {
		t.Errorf("extractIcon at last index %d failed", n-1)
	} else {
		destroyIcon(h)
	}
}

func TestIconCacheReturnsSameHandle(t *testing.T) {
	defer purgeIcons()
	const dll = `C:\Windows\System32\imageres.dll`

	a := iconFor(dll, 0, 32)
	b := iconFor(dll, 0, 32)
	if a == 0 {
		t.Fatal("iconFor returned no handle")
	}
	if a != b {
		t.Error("repeated iconFor for the same key must hit the cache")
	}
	// A different size is a different cache entry, which is what makes moving
	// between differently-scaled monitors correct rather than blurry.
	if c := iconFor(dll, 0, 16); c == a {
		t.Error("a different size must produce a separately extracted icon")
	}
}

func TestIconForMissingFileFallsBack(t *testing.T) {
	defer purgeIcons()
	h := iconFor(`C:\definitely\not\here.dll`, 0, 32)
	if h == 0 {
		t.Error("a missing icon file must fall back, never drop the item")
	}
	if h != genericIcon() {
		t.Error("expected the shared generic icon")
	}
}

// The acceptance test for "non-exe targets get the correct associated icon":
// each target type must resolve to a source that actually yields pixels, not
// to the generic fallback.
//
// The two shell APIs each cover only half of these -- SHGFI_ICONLOCATION points
// a document back at itself, ASSOCSTR_DEFAULTICON knows nothing about a
// specific file -- so this is what pins the ordering in resolveIconSource.
func TestIconResolutionByTargetType(t *testing.T) {
	defer purgeIcons()

	tmp := t.TempDir()
	txt := filepath.Join(tmp, "notes.txt")
	if err := os.WriteFile(txt, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		exec string
	}{
		{"executable", `C:\Windows\System32\notepad.exe`},
		{"dll", `C:\Windows\System32\imageres.dll`},
		{"text document", txt},
		{"folder", tmp},
		{"url", "https://example.com/"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file, idx := resolveIconSource(c.exec, "")
			if file == "" {
				t.Fatalf("no icon source resolved for %s", c.exec)
			}
			t.Logf("%s -> %s,%d", c.exec, file, idx)

			h := iconFor(file, idx, 32)
			if h == 0 {
				t.Fatalf("no icon handle for %s,%d", file, idx)
			}
			if h == genericIcon() {
				t.Errorf("%s fell back to the generic icon (source %s,%d)", c.exec, file, idx)
			}
		})
	}
}

// The .xlsx entries in the migrated config depend on this specifically: Excel's
// document icon lives in xlicons.exe, which only the type association reports.
func TestExcelDocumentIconResolvesToExcelsIconFile(t *testing.T) {
	defer purgeIcons()

	const doc = `C:\Data\Dateien\Finanzen\Finanzplan.xlsx`
	if _, err := os.Stat(doc); err != nil {
		t.Skipf("%s not present", doc)
	}

	file, idx := resolveIconSource(doc, "")
	if samePath(file, doc) {
		t.Fatalf("icon source is the document itself (%s), which holds no icon", file)
	}
	t.Logf("%s -> %s,%d", doc, file, idx)

	if h := iconFor(file, idx, 32); h == 0 || h == genericIcon() {
		t.Errorf("could not extract a real icon from %s,%d", file, idx)
	}
}

func TestIsAppsFolder(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`shell:AppsFolder\Microsoft.Windows.Photos_8wekyb3d8bbwe!App`, true},
		{`SHELL:appsfolder\X_y!App`, true}, // case-insensitive
		{`C:\Windows\notepad.exe`, false},
		{`shell:Downloads`, false},
		{`https://example.com/`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := isAppsFolder(c.in); got != c.want {
			t.Errorf("isAppsFolder(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A Store app's moniker must survive resolveIconSource untouched, so iconFor can
// route it to the shell image factory instead of icon-resource extraction.
func TestResolveIconSourceKeepsAppsFolderMoniker(t *testing.T) {
	const moniker = `shell:AppsFolder\Microsoft.Windows.Photos_8wekyb3d8bbwe!App`
	file, idx := resolveIconSource(moniker, "")
	if file != moniker || idx != 0 {
		t.Errorf("resolveIconSource(%q) = (%q, %d), want the moniker unchanged", moniker, file, idx)
	}
	// An explicit icon still overrides the moniker.
	if f, _ := resolveIconSource(moniker, `C:\Windows\System32\imageres.dll,3`); f != `C:\Windows\System32\imageres.dll` {
		t.Errorf("explicit icon should override a Store app moniker, got %q", f)
	}
}

// End to end: a Store app that ships with Windows must yield a real icon from
// the shell image factory, not the generic fallback. Skipped where the app is
// absent so the suite still passes on a stripped-down image.
func TestStoreAppIconResolves(t *testing.T) {
	defer purgeIcons()
	coInitialize()

	candidates := []string{
		`shell:AppsFolder\Microsoft.Windows.Photos_8wekyb3d8bbwe!App`,
		`shell:AppsFolder\Microsoft.WindowsCalculator_8wekyb3d8bbwe!App`,
		`shell:AppsFolder\Microsoft.WindowsStore_8wekyb3d8bbwe!App`,
	}
	for _, moniker := range candidates {
		if h := shellImageIcon(moniker, 32); h != 0 {
			t.Logf("%s -> icon handle %#x", moniker, h)
			destroyIcon(h)
			return
		}
	}
	t.Skip("none of the probed Store apps are installed")
}

func TestJSONEscape(t *testing.T) {
	got := jsonEscape(`C:\Windows\System32\imageres.dll`)
	want := `C:\\Windows\\System32\\imageres.dll`
	if got != want {
		t.Errorf("jsonEscape = %q, want %q", got, want)
	}
}
