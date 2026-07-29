//go:build windows

package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StringTable is every user-visible string this program produces outside of
// config.json itself: the tray menu, the config-error/warning menu,
// notifications, the tray tooltip, the power-action confirmation dialogs, and
// the default labels for special.go's catalog and settingsPages (the
// Specials/SettingsGroups/SettingsPages fields below). It does not cover
// config.json's own entries -- those always show whatever label the config
// gives them, which wins over every field here.
//
// A field whose name ends in "Format" is a fmt.Sprintf template; a
// translation may reorder which placeholder fills which slot with Go's
// explicit-index syntax (%[2]s ... %[1]s) if word order needs it, but must
// keep the same verbs and the same count -- Sprintf fills them positionally.
//
// The product name ("TaskbarMenu") is deliberately not a field: it is the
// windowTitle constant and stays that literal string in every language.
type StringTable struct {
	TrayShowMenu     string `json:"trayShowMenu"`
	TrayReloadConfig string `json:"trayReloadConfig"`
	TrayEditConfig   string `json:"trayEditConfig"`
	TrayExit         string `json:"trayExit"`

	ErrorConfigError string `json:"errorConfigError"`
	ErrorOpenConfig  string `json:"errorOpenConfig"`
	ErrorReload      string `json:"errorReload"`
	ErrorNoEntries   string `json:"errorNoEntries"` // %s = config file name
	WarningFormat    string `json:"warningFormat"`  // %d = warning count

	OpenAll          string `json:"openAll"`
	OpenFolderFormat string `json:"openFolderFormat"` // %s = the entry's own label
	EmptyFolder      string `json:"emptyFolder"`
	More             string `json:"more"`

	NotifyLaunchFailedFormat     string `json:"notifyLaunchFailedFormat"`     // %s label, %s error
	NotifyConfigErrorFormat      string `json:"notifyConfigErrorFormat"`      // %s error
	NotifyReloadedWarningsFormat string `json:"notifyReloadedWarningsFormat"` // %s file, %d count
	NotifyReloadedFormat         string `json:"notifyReloadedFormat"`         // %s file
	NotifyPowerFailedFormat      string `json:"notifyPowerFailedFormat"`      // %s verb, %s error

	TooltipFormat         string `json:"tooltipFormat"`         // %s brand, %s file
	TooltipErrorFormat    string `json:"tooltipErrorFormat"`    // %s brand, %s file
	TooltipWarningsFormat string `json:"tooltipWarningsFormat"` // %s brand, %s file, %d count

	PowerPromptSignOut   string `json:"powerPromptSignOut"`
	PowerPromptRestart   string `json:"powerPromptRestart"`
	PowerPromptShutdown  string `json:"powerPromptShutdown"`
	PowerPromptSleep     string `json:"powerPromptSleep"`
	PowerPromptHibernate string `json:"powerPromptHibernate"`
	PowerPromptLock      string `json:"powerPromptLock"`

	// Verbs substituted into NotifyPowerFailedFormat, so "Could not shut
	// down" reads naturally instead of leaking the internal catalog id
	// ("shutdown") into a translated sentence.
	PowerVerbLock      string `json:"powerVerbLock"`
	PowerVerbSignOut   string `json:"powerVerbSignOut"`
	PowerVerbRestart   string `json:"powerVerbRestart"`
	PowerVerbShutdown  string `json:"powerVerbShutdown"`
	PowerVerbSleep     string `json:"powerVerbSleep"`
	PowerVerbHibernate string `json:"powerVerbHibernate"`

	// Specials, SettingsGroups and SettingsPages are the exhaustive label
	// source for special.go's catalog: special.go carries no English text of
	// its own (see specialEntry/settingsPage), so every id/uri/group here
	// must resolve for every shipped language; en.json is the authoritative
	// copy and de.json a full, standalone translation, not a delta over it.
	// Keyed by each catalog's own stable identifier (specialEntry.id,
	// settingsPage.group, settingsPage.uri), never by English text. The
	// user's own label in config.json always wins regardless of what these
	// maps hold.
	Specials       map[string]string `json:"specials,omitempty"`
	SettingsGroups map[string]string `json:"settingsGroups,omitempty"`
	SettingsPages  map[string]string `json:"settingsPages,omitempty"`
}

//go:embed lang/*.json
var embeddedLang embed.FS

var ui = englishDefaults()

func englishDefaults() StringTable {
	var s StringTable
	raw, err := embeddedLang.ReadFile("lang/en.json")
	if err != nil {
		panic("lang: embedded lang/en.json missing: " + err.Error())
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		panic("lang: embedded lang/en.json malformed: " + err.Error())
	}
	return s
}

// detectUserLocale is a seam so a test can supply a locale without depending
// on the real API or the test host's own language -- the same reasoning as
// the powerExec seam in power.go.
var detectUserLocale = func() string { return getUserDefaultLocaleName() }

// langResolution is what resolveLanguage decided.
type langResolution struct {
	strings    StringTable
	tag        string // resolved tag; "en" on any fallback
	watch      string // external file reload() should stamp-check; "" if none in play
	warning    string // non-fatal problem worth a config warning; "" if none
	unresolved bool   // an explicit (non-"auto") request could not be honoured at all
}

// resolveLanguage decides which language to use and loads it. Always returns
// a complete, usable StringTable -- any failure (no match, unreadable file,
// malformed JSON, a failed locale query) degrades to embedded English, so it
// can never block startup or blank out the menu.
//
// setting is Config.Language ("auto", or an explicit tag); exeDir is the
// directory external lang/<tag>.json files resolve against (filepath.Dir of
// the exe -- the same rule config.json's own folder uses; see
// defaultConfigPath in main.go). exeDir is "" when the exe path is unknown,
// in which case only embedded languages are considered.
func resolveLanguage(setting, exeDir string) langResolution {
	req := strings.ToLower(strings.TrimSpace(setting))
	explicit := req != "" && req != "auto"
	if !explicit {
		req = strings.ToLower(detectUserLocale())
	}

	if req != "" {
		if r, ok := tryLangTag(req, exeDir); ok {
			return r
		}
		if i := strings.IndexByte(req, '-'); i > 0 { // "de-DE" -> "de"
			if r, ok := tryLangTag(req[:i], exeDir); ok {
				return r
			}
		}
	}

	r, _ := tryLangTag("en", exeDir) // always succeeds: lang/en.json is always embedded
	r.unresolved = explicit
	return r
}

func tryLangTag(tag, exeDir string) (langResolution, bool) {
	s, found, badExternal := loadLangTag(tag, exeDir)
	if !found {
		return langResolution{}, false
	}
	r := langResolution{strings: s, tag: tag, watch: watchPath(exeDir, tag)}
	if badExternal {
		r.warning = fmt.Sprintf("language: lang/%s.json exists but is not valid JSON, ignoring it", tag)
	}
	return r, true
}

func watchPath(exeDir, tag string) string {
	if exeDir == "" {
		return ""
	}
	return filepath.Join(exeDir, "lang", tag+".json")
}

// loadLangTag layers one language's StringTable: embedded English defaults,
// then an embedded lang/<tag>.json if the binary ships one, then an external
// lang/<tag>.json next to the exe if one exists -- each layer only
// overwriting the keys it actually sets. Mirrors LoadConfig's own
// defaults-then-decode pattern (config.go:114-141): a struct pre-populated
// with defaults, decoded onto, so an absent key keeps its prior value.
//
// found reports whether at least one layer beyond English applied.
// badExternal reports an external file that exists but is not valid JSON.
func loadLangTag(tag, exeDir string) (s StringTable, found, badExternal bool) {
	s = englishDefaults()
	name := "lang/" + tag + ".json"

	if raw, err := embeddedLang.ReadFile(name); err == nil {
		if json.Unmarshal(raw, &s) == nil {
			found = true
		}
	}
	if exeDir == "" {
		return s, found, false
	}
	raw, err := os.ReadFile(filepath.Join(exeDir, "lang", tag+".json"))
	if err != nil {
		return s, found, false
	}
	if json.Unmarshal(decodeBOM(raw), &s) == nil {
		return s, true, false
	}
	return s, found, true
}

// specialLabel returns the active language's label for a catalog entry --
// called only where the config gave no explicit label of its own. special.go
// carries no English text, so this always reads ui.Specials; e.id is the
// fallback only for the impossible case of an id missing from en.json
// entirely (a build defect englishDefaults' own tests would already have
// caught), so a gap is visible and debuggable rather than a blank menu item.
func specialLabel(e specialEntry) string {
	if t, ok := ui.Specials[e.id]; ok && t != "" {
		return t
	}
	return e.id
}

// settingsGroupLabel returns the active language's label for one of the
// allSettings submenu's group headers. As with specialLabel, special.go
// carries no English text for a group, so this always reads
// ui.SettingsGroups; the identifier is the fallback only for a group missing
// from en.json entirely.
func settingsGroupLabel(group string) string {
	if t, ok := ui.SettingsGroups[group]; ok && t != "" {
		return t
	}
	return group
}

// settingsPageLabel returns the active language's label for one
// settingsPages entry. As with specialLabel, special.go carries no English
// text for a page, so this always reads ui.SettingsPages; the uri is the
// fallback only for an id missing from en.json entirely.
func settingsPageLabel(s settingsPage) string {
	if t, ok := ui.SettingsPages[s.uri]; ok && t != "" {
		return t
	}
	return s.uri
}
