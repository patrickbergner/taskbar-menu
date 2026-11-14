//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression tests for the fixes from review.md, grouped here so each one
// names the finding it pins.

// Finding 1: a special without its own label must be named in the config's
// language on the very first load, not only after a second reload.
func TestReloadNamesSpecialsInConfigLanguageOnFirstLoad(t *testing.T) {
	savedUI, savedLocale := ui, detectUserLocale
	t.Cleanup(func() { ui, detectUserLocale = savedUI, savedLocale })
	detectUserLocale = func() string { return "de-DE" }
	ui = englishDefaults() // what a freshly started process holds

	path := filepath.Join(t.TempDir(), "config.json")
	mustWrite(t, path, `{"items":[{"special":"shutdown"}]}`)
	a := &appState{cfgPath: path, byID: map[uint32]*node{}}
	a.reload(true)

	want := ui.Specials["shutdown"]
	if want == englishDefaults().Specials["shutdown"] {
		t.Fatalf("test setup: German label %q is the same as the English one", want)
	}
	if got := a.cfg.Items[0].Label; got != want {
		t.Errorf("first load label = %q, want the German %q", got, want)
	}
}

// Finding 1, the explicit-language variant: switching "language" takes effect
// on the load that reads it.
func TestLoadConfigLocalizedUsesTheFilesOwnLanguage(t *testing.T) {
	saved := ui
	t.Cleanup(func() { ui = saved })
	ui = englishDefaults()

	path := filepath.Join(t.TempDir(), "config.json")
	mustWrite(t, path, `{"language":"de","items":[{"special":"lock"}]}`)
	cfg, lr, err := loadConfigLocalized(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if lr.tag != "de" {
		t.Fatalf("tag = %q, want de", lr.tag)
	}
	if got, want := cfg.Items[0].Label, ui.Specials["lock"]; got != want {
		t.Errorf("label = %q, want %q", got, want)
	}
}

// Finding 5: a budget exhausted while listing one level cut nothing off, so
// there must be no "More…" entry.
func TestSpentBudgetAloneAddsNoMoreNode(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		mustWrite(t, filepath.Join(dir, n), "x")
	}
	a := newTestApp()
	b := &walkBudget{nodes: 1, deadline: time.Now().Add(time.Minute)}
	got := labels(a.expandFolder([]string{dir}, 1, defaultLimit, sortNameAsc, b))
	if len(got) != 3 {
		t.Errorf("got %v, want the three files and nothing else", got)
	}
}

// Finding 6: the timeout wrapper must still return an ordinary listing.
func TestReadDirWithinReturnsListing(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "a")
	ents, err := readDirWithin(dir, readDirTimeout)
	if err != nil || len(ents) != 1 || ents[0].Name() != "a.txt" {
		t.Errorf("got %v, %v; want [a.txt], nil", ents, err)
	}
	if _, err := readDirWithin(filepath.Join(dir, "missing"), readDirTimeout); err == nil || err == errReadDirTimeout {
		t.Errorf("a missing directory should report its own error, got %v", err)
	}
}

// Finding 7: "Foo.lnk" and "Foo.url" in one folder are two entries; the same
// name in a later root is still a duplicate.
func TestMergeLevelsKeepsSameNameFilesOfOneFolder(t *testing.T) {
	user := []dirEntry{
		{label: "Foo", path: `C:\u\Foo.lnk`},
		{label: "Foo", path: `C:\u\Foo.url`},
	}
	all := []dirEntry{
		{label: "foo", path: `C:\a\Foo.lnk`},
	}
	got := mergeLevels([][]dirEntry{user, all})
	if len(got) != 2 || got[0].path != `C:\u\Foo.lnk` || got[1].path != `C:\u\Foo.url` {
		t.Errorf("got %+v, want both per-user files and not the all-users copy", got)
	}
}

// Finding 10: the user folders are resolved through the known-folder API.
func TestResolveRootKnownFolders(t *testing.T) {
	for name := range knownFolders {
		p := resolveRoot(knownFolderPrefix + name)
		if p == "" || strings.HasPrefix(p, knownFolderPrefix) {
			t.Errorf("%s: resolved to %q", name, p)
			continue
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			t.Logf("%s: %q is not a directory on this machine (%v)", name, p, err)
		}
	}
	if p := resolveRoot(knownFolderPrefix + "NoSuchFolder"); p != "" {
		t.Errorf("an unknown known folder should resolve to nothing, got %q", p)
	}
	if p := resolveRoot(`%SystemRoot%\x`); strings.Contains(p, "%") {
		t.Errorf("a plain root should still be env-expanded, got %q", p)
	}
	for _, id := range []string{"desktop", "documents", "downloads", "pictures"} {
		d := dynSourceFor(Item{Special: id, Label: id})
		if d == nil || len(d.roots) == 0 {
			t.Errorf("%s: no roots", id)
			continue
		}
		for _, r := range d.roots {
			if strings.HasPrefix(r, knownFolderPrefix) || strings.Contains(r, "%") {
				t.Errorf("%s: unresolved root %q", id, r)
			}
		}
	}
}

// Finding 11: launcher ids that share a taskbar identity or a file name are
// reported, and an exact duplicate id is called out as such.
func TestLauncherCollisionsAreWarned(t *testing.T) {
	path := writeConfig(t, `{"launchers":[
		{"id":"my app","exec":"a.exe"},
		{"id":"my-app","exec":"b.exe"},
		{"id":"a:b","exec":"c.exe"},
		{"id":"a?b","exec":"d.exe"},
		{"id":"dup","exec":"e.exe"},
		{"id":"dup","exec":"f.exe"},
		{"id":"Clean1","exec":"g.exe"},
		{"id":"Clean2","exec":"h.exe"}
	]}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(cfg.Warnings, "\n")
	for _, want := range []string{
		`"my-app"): same taskbar identity as "my app"`,
		`"a?b"): same shortcut file name as "a:b"`,
		`"dup"): duplicate id`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing warning containing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "Clean") {
		t.Errorf("distinct ids must not be warned about:\n%s", all)
	}
}
