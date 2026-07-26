//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// assertComplete sweeps every string field via reflection, so a newly added
// StringTable field is automatically covered rather than needing this test
// hand-updated.
func assertComplete(t *testing.T, s StringTable, name string) {
	t.Helper()
	v := reflect.ValueOf(s)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() != reflect.String {
			continue // Specials/SettingsGroups/SettingsPages are maps, not required
		}
		if f.String() == "" {
			t.Errorf("%s: field %s is empty", name, v.Type().Field(i).Name)
		}
	}
}

func TestEnglishDefaultsComplete(t *testing.T) {
	assertComplete(t, englishDefaults(), "lang/en.json")
}

// German must be a full, standalone translation for every chrome string --
// decoded into a blank StringTable, not merged over English first, so a
// missing key shows up here rather than being silently papered over.
func TestGermanTranslationComplete(t *testing.T) {
	raw, err := embeddedLang.ReadFile("lang/de.json")
	if err != nil {
		t.Fatal(err)
	}
	var s StringTable
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	assertComplete(t, s, "lang/de.json")
}

// Every specials/settingsPages translation key must match a real catalog
// key, so a typo doesn't silently fail closed to English with no signal.
func TestGermanCatalogTranslationsMatchRealKeys(t *testing.T) {
	raw, err := embeddedLang.ReadFile("lang/de.json")
	if err != nil {
		t.Fatal(err)
	}
	var s StringTable
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}

	ids := map[string]bool{}
	for _, e := range specials {
		ids[e.id] = true
	}
	for key := range s.Specials {
		if !ids[key] {
			t.Errorf("specials[%q]: no such id in special.go", key)
		}
	}

	groups := map[string]bool{}
	uris := map[string]bool{}
	for _, p := range settingsPages {
		groups[p.group] = true
		uris[p.uri] = true
	}
	for key := range s.SettingsGroups {
		if !groups[key] {
			t.Errorf("settingsGroups[%q]: no such group in settingsPages", key)
		}
	}
	for key := range s.SettingsPages {
		if !uris[key] {
			t.Errorf("settingsPages[%q]: no such uri in settingsPages", key)
		}
	}
}

// special.go carries no English text of its own (see specialEntry and
// settingsPage), so unlike German -- which only needs to avoid stale keys --
// en.json's specials, settingsGroups and settingsPages maps must be
// exhaustive: every id, group and uri the catalog defines needs an entry
// here, or specialLabel/settingsGroupLabel/settingsPageLabel fall back to
// printing the raw identifier in the menu.
func TestEnglishCatalogTranslationsComplete(t *testing.T) {
	en := englishDefaults()

	for _, e := range specials {
		if en.Specials[e.id] == "" {
			t.Errorf("lang/en.json: specials[%q] missing (special.go id with no English label)", e.id)
		}
	}
	for key := range en.Specials {
		if _, ok := specialIndex[strings.ToLower(key)]; !ok {
			t.Errorf("lang/en.json: specials[%q] has no matching id in special.go", key)
		}
	}

	groups := map[string]bool{}
	uris := map[string]bool{}
	for _, p := range settingsPages {
		groups[p.group] = true
		uris[p.uri] = true
		if en.SettingsPages[p.uri] == "" {
			t.Errorf("lang/en.json: settingsPages[%q] missing (settingsPages uri with no English label)", p.uri)
		}
	}
	for key := range en.SettingsPages {
		if !uris[key] {
			t.Errorf("lang/en.json: settingsPages[%q] has no matching uri in settingsPages", key)
		}
	}
	for g := range groups {
		if en.SettingsGroups[g] == "" {
			t.Errorf("lang/en.json: settingsGroups[%q] missing (settingsPages group with no English label)", g)
		}
	}
	for key := range en.SettingsGroups {
		if !groups[key] {
			t.Errorf("lang/en.json: settingsGroups[%q] has no matching group in settingsPages", key)
		}
	}
}

func TestResolveLanguageAutoFallsBackToEnglishWhenUnsupported(t *testing.T) {
	saved := detectUserLocale
	detectUserLocale = func() string { return "fr-FR" }
	t.Cleanup(func() { detectUserLocale = saved })

	lr := resolveLanguage("auto", t.TempDir())
	if lr.tag != "en" || lr.unresolved {
		t.Errorf("got tag=%q unresolved=%v, want en/false (auto degrading silently)", lr.tag, lr.unresolved)
	}
}

func TestResolveLanguageExplicitTagLoadsEmbeddedGerman(t *testing.T) {
	lr := resolveLanguage("de", "")
	if lr.tag != "de" || lr.unresolved || lr.strings.TrayExit != "Beenden" {
		t.Errorf("got %+v", lr)
	}
}

func TestResolveLanguageFullTagFallsBackToPrimarySubtag(t *testing.T) {
	lr := resolveLanguage("de-DE", "") // no de-DE.json anywhere, only embedded de.json
	if lr.tag != "de" || lr.unresolved {
		t.Errorf("got tag=%q unresolved=%v, want de/false", lr.tag, lr.unresolved)
	}
}

func TestResolveLanguageExplicitUnknownTagWarnsAndFallsBackToEnglish(t *testing.T) {
	lr := resolveLanguage("xx", "")
	if lr.tag != "en" || !lr.unresolved {
		t.Errorf("got tag=%q unresolved=%v, want en/true", lr.tag, lr.unresolved)
	}
}

// The load-order contract: a partial external override sits on top of the
// FULL embedded translation for that tag, not on top of bare English.
func TestResolveLanguageExternalFilePartiallyOverridesEmbedded(t *testing.T) {
	dir := t.TempDir()
	mustWriteLang(t, dir, "de.json", `{"trayExit":"Custom Exit"}`)

	lr := resolveLanguage("de", dir)
	if lr.strings.TrayExit != "Custom Exit" {
		t.Errorf("TrayExit = %q, want the override", lr.strings.TrayExit)
	}
	if lr.strings.TrayShowMenu != "Menü anzeigen" {
		t.Errorf("TrayShowMenu = %q, want the untouched embedded German", lr.strings.TrayShowMenu)
	}
	if lr.watch != filepath.Join(dir, "lang", "de.json") {
		t.Errorf("watch = %q", lr.watch)
	}
}

// A brand-new tag with no embedded counterpart still resolves purely from an
// external file, so a user-authored third language works with no rebuild.
func TestResolveLanguageExternalOnlyNewTag(t *testing.T) {
	dir := t.TempDir()
	mustWriteLang(t, dir, "fr.json", `{"trayExit":"Quitter"}`)

	lr := resolveLanguage("fr", dir)
	if lr.tag != "fr" || lr.unresolved || lr.strings.TrayExit != "Quitter" {
		t.Errorf("got %+v", lr)
	}
	if lr.strings.TrayShowMenu != "Show menu" { // missing key: falls back to English
		t.Errorf("TrayShowMenu = %q, want the English fallback", lr.strings.TrayShowMenu)
	}
}

func TestResolveLanguageMalformedExternalFileWarnsAndFallsBackToEmbedded(t *testing.T) {
	dir := t.TempDir()
	mustWriteLang(t, dir, "de.json", `{ not json`)

	lr := resolveLanguage("de", dir)
	if lr.tag != "de" || lr.warning == "" {
		t.Errorf("got tag=%q warning=%q, want de/non-empty", lr.tag, lr.warning)
	}
	if lr.strings.TrayExit != "Beenden" {
		t.Errorf("should still fall back to the valid embedded German, got %q", lr.strings.TrayExit)
	}
}

// reload()'s hot-reload extension: editing the active external lang file and
// forcing a reload must be picked up, exactly like editing config.json is.
func TestReloadPicksUpLanguageFileEditOnForcedReload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	mustWrite(t, cfgPath, `{"language":"de","items":[{"label":"A","exec":"a.exe"}]}`)
	mustWriteLang(t, dir, "de.json", `{"trayExit":"Erst"}`)

	saved := ui
	t.Cleanup(func() { ui = saved })

	a := &appState{cfgPath: cfgPath, exePath: filepath.Join(dir, "TaskbarMenu.exe"), byID: map[uint32]*node{}}
	a.reload(true)
	if ui.TrayExit != "Erst" {
		t.Fatalf("TrayExit = %q, want Erst", ui.TrayExit)
	}

	mustWriteLang(t, dir, "de.json", `{"trayExit":"Zweitens"}`)
	a.reload(true) // "Reload config" always forces, regardless of any stamp
	if ui.TrayExit != "Zweitens" {
		t.Errorf("TrayExit = %q, want Zweitens after the edit", ui.TrayExit)
	}
}

func mustWriteLang(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "lang"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "lang", name), body)
}
