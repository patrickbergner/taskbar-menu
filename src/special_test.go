//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The catalog is hand-maintained data, so the invariants a reader assumes when
// scanning the table are asserted rather than trusted.
func TestSpecialCatalogWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range specials {
		if e.id == "" || e.label == "" || e.group == "" {
			t.Errorf("incomplete entry: %+v", e)
			continue
		}
		key := strings.ToLower(e.id)
		if seen[key] {
			t.Errorf("duplicate id %q (ids are compared case-insensitively)", e.id)
		}
		seen[key] = true

		if e.kind == kindExec && e.target == "" {
			t.Errorf("%s: kindExec with no target", e.id)
		}
		// A literal drive letter would break on a machine whose Windows is not
		// on C:, which is exactly the kind of bug nobody hits until someone does.
		for _, p := range []string{e.target, e.icon} {
			if len(p) > 1 && p[1] == ':' {
				t.Errorf("%s: %q hardcodes a drive letter, use %%SystemRoot%% etc.", e.id, p)
			}
		}
	}
}

// Every alias must name a real entry, or it reports "unknown special" and is
// simply a lie in the table.
func TestSpecialAliasesResolve(t *testing.T) {
	for alias, target := range specialAliases {
		if _, ok := specialIndex[strings.ToLower(target)]; !ok {
			t.Errorf("alias %q points at unknown id %q", alias, target)
		}
		if _, ok := lookupSpecial(alias); !ok {
			t.Errorf("alias %q does not resolve", alias)
		}
	}
}

func TestSpecialLookupIsCaseInsensitive(t *testing.T) {
	for _, id := range []string{"deviceManager", "devicemanager", "DEVICEMANAGER", "  DeviceManager  "} {
		e, ok := lookupSpecial(id)
		if !ok || e.id != "deviceManager" {
			t.Errorf("lookupSpecial(%q) = %q, %v; want deviceManager, true", id, e.id, ok)
		}
	}
	if _, ok := lookupSpecial(""); ok {
		t.Error("empty id must not resolve")
	}
}

func TestSpecialSuppliesLabelAndIcon(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"items":[{"special":"taskManager"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Items) != 1 {
		t.Fatalf("got %d items, want 1 (warnings: %v)", len(cfg.Items), cfg.Warnings)
	}
	if cfg.Items[0].Label != "Task Manager" {
		t.Errorf("Label = %q, want %q", cfg.Items[0].Label, "Task Manager")
	}
	if !strings.HasSuffix(strings.ToLower(cfg.Items[0].Exec), `\taskmgr.exe`) {
		t.Errorf("Exec = %q, want it to end in \\taskmgr.exe", cfg.Items[0].Exec)
	}
}

func TestSpecialExplicitLabelAndIconWin(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"special":"deviceManager","label":"Geräte","icon":"C:\\x.dll,7"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	it := cfg.Items[0]
	if it.Label != "Geräte" {
		t.Errorf("Label = %q, want the explicit one", it.Label)
	}
	if it.Icon != `C:\x.dll,7` {
		t.Errorf("Icon = %q, want the explicit one", it.Icon)
	}
}

// The design decision that keeps buildNodes, launch, printItems and shortcutIcon
// free of any knowledge of specials: a kindExec row leaves nothing behind.
func TestExecSpecialIsFullyDesugared(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"items":[{"special":"deviceManager"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Items[0].Special != "" {
		t.Errorf("Special = %q, want it cleared after normalisation", cfg.Items[0].Special)
	}
	if cfg.Items[0].Exec == "" {
		t.Error("Exec was not filled in from the catalog")
	}
	if strings.Contains(cfg.Items[0].Exec, "%") {
		t.Errorf("Exec = %q, still holds an unexpanded %%VAR%%", cfg.Items[0].Exec)
	}
}

// environmentVariables is the entry that proves catalog args survive: the
// rundll32 argument is one token and must not be split or re-quoted.
func TestSpecialSuppliesArgs(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"items":[{"special":"environmentVariables"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	args := cfg.Items[0].Args
	if len(args) != 1 || args[0] != "sysdm.cpl,EditEnvironmentVariables" {
		t.Errorf("Args = %q, want one element sysdm.cpl,EditEnvironmentVariables", args)
	}
}

func TestSpecialExplicitArgsWin(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"special":"resourceMonitor","args":["/custom"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Items[0].Args; len(got) != 1 || got[0] != "/custom" {
		t.Errorf("Args = %q, want the explicit ones", got)
	}
}

// The catalog must not be aliased into the item: rewriting one entry's args must
// not affect the next entry naming the same special.
func TestSpecialArgsAreCopied(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"special":"environmentVariables"},{"special":"environmentVariables"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Items[0].Args[0] = "mutated"
	if cfg.Items[1].Args[0] != "sysdm.cpl,EditEnvironmentVariables" {
		t.Error("two entries share the catalog's args slice")
	}
	if e, _ := lookupSpecial("environmentVariables"); e.args[0] != "sysdm.cpl,EditEnvironmentVariables" {
		t.Errorf("the catalog itself was mutated: %q", e.args[0])
	}
}

// Same forward-compatibility policy as TestUnknownTypeIsSkippedNotFatal: a
// config written for a later build still opens.
func TestUnknownSpecialIsSkippedNotFatal(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"special":"nosuchthing"},{"label":"Good","exec":"a.exe"}]}`))
	if err != nil {
		t.Fatalf("unknown special must not fail the file: %v", err)
	}
	if len(cfg.Items) != 1 || cfg.Items[0].Label != "Good" {
		t.Errorf("items = %+v, want only the good one", cfg.Items)
	}
	if len(cfg.Warnings) != 1 {
		t.Errorf("warnings = %v, want exactly one", cfg.Warnings)
	}
}

func TestSpecialBeatsExecAndAppID(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"special":"taskManager","exec":"other.exe","appId":"Some!App"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	it := cfg.Items[0]
	if it.AppID != "" {
		t.Errorf("AppID = %q, want it cleared", it.AppID)
	}
	if !strings.HasSuffix(strings.ToLower(it.Exec), `\taskmgr.exe`) {
		t.Errorf("Exec = %q, want the catalog target", it.Exec)
	}
	if len(cfg.Warnings) != 1 {
		t.Errorf("warnings = %v, want exactly one", cfg.Warnings)
	}
}

func TestItemsBeatSpecial(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"label":"Sub","special":"taskManager","items":[{"label":"A","exec":"a.exe"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	it := cfg.Items[0]
	if it.Exec != "" || len(it.Items) != 1 {
		t.Errorf("items should win intact, got %+v", it)
	}
	if len(cfg.Warnings) != 1 {
		t.Errorf("warnings = %v, want exactly one", cfg.Warnings)
	}
}

func TestLauncherAcceptsSpecial(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"launchers":[{"id":"dm","special":"deviceManager"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Launchers) != 1 {
		t.Fatalf("launchers = %+v, warnings %v", cfg.Launchers, cfg.Warnings)
	}
	l := cfg.Launchers[0]
	if l.Id != "dm" {
		t.Errorf("Id = %q, want the launcher's own id", l.Id)
	}
	if l.Label != "Device Manager" || l.Special != "" || l.Exec == "" || l.Icon == "" {
		t.Errorf("launcher not desugared: %+v", l)
	}
}

func TestLauncherUnknownSpecialIsSkipped(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"launchers":[{"id":"x","special":"nope"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Launchers) != 0 || len(cfg.Warnings) != 1 {
		t.Errorf("launchers = %+v, warnings = %v", cfg.Launchers, cfg.Warnings)
	}
}

func TestIsShellMoniker(t *testing.T) {
	cases := map[string]bool{
		`shell:RecycleBinFolder`:                   true,
		`SHELL:AppsFolder\Foo!App`:                 true,
		`  shell:Fonts`:                            true,
		`::{20D04FE0-3AEA-1069-A2D8-08002B30309D}`: true,
		`C:\Windows\System32\shell32.dll`:          false,
		`https://example.com/`:                     false,
		``:                                         false,
	}
	for in, want := range cases {
		if got := isShellMoniker(in); got != want {
			t.Errorf("isShellMoniker(%q) = %v, want %v", in, got, want)
		}
	}
}

// The catalog's whole value is icons that resolve to something real, and that is
// exactly what --config-check cannot tell you: it prints the spec, not the result.
//
// Deliberately does NOT go through iconFor. iconFor retries index 0 when the
// requested index is missing (icons.go), which is the right behaviour for a
// user's own typo but would make this test pass for every wrong resource id in
// the table -- the one mistake it exists to catch. So it calls the same two
// extractors iconFor dispatches to, minus the safety net.
//
// Needs the live system, so a module missing on this SKU is skipped rather than
// failed: gpedit.dll and secpol.msc are absent on Home editions.
func TestCatalogIconsExtract(t *testing.T) {
	coInitialize()

	for _, e := range specials {
		if e.kind != kindExec {
			continue
		}
		exec, _, icon := e.resolved()
		file, idx := resolveIconSource(exec, icon)
		if file == "" {
			t.Errorf("%s: resolves to no icon source at all", e.id)
			continue
		}

		if isShellMoniker(file) {
			if h := shellImageIcon(file, 16); h == 0 {
				t.Errorf("%s: shell moniker %s yields no image", e.id, file)
			} else {
				destroyIcon(h)
			}
			continue
		}

		if _, err := os.Stat(file); err != nil {
			t.Logf("%s: skipped, %s not present on this machine", e.id, file)
			continue
		}
		h := extractIcon(file, idx, 16)
		if h == 0 {
			t.Errorf("%s: %s,%d extracts nothing (wrong index or resource id?)", e.id, file, idx)
			continue
		}
		destroyIcon(h)
	}
}

// The schema's enum is what gives editors completion, and it is maintained by
// hand in a different file from the catalog. Nothing but a test stops the two
// from drifting apart.
func TestSchemaSpecialEnumMatchesCatalog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "config.schema.json"))
	if err != nil {
		t.Skipf("schema not readable: %v", err)
	}
	var doc struct {
		Definitions struct {
			SpecialID struct {
				Enum []string `json:"enum"`
			} `json:"specialId"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	// Aliases belong in the enum too: they resolve at runtime, so leaving them
	// out means an editor underlines a config that works perfectly.
	want := map[string]bool{}
	for _, e := range specials {
		want[e.id] = true
	}
	for alias := range specialAliases {
		want[alias] = true
	}

	got := map[string]bool{}
	for _, id := range doc.Definitions.SpecialID.Enum {
		got[id] = true
	}

	for id := range want {
		if !got[id] {
			t.Errorf("%q is accepted at runtime but missing from the schema enum", id)
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("schema enum has %q, which is neither a catalog id nor an alias", id)
		}
	}
}

// config.example.json is documentation people copy from, so it must actually
// load without warnings on any machine.
func TestExampleConfigNormalisesCleanly(t *testing.T) {
	path := filepath.Join("..", "config.example.json")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("example not present: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("example config does not load: %v", err)
	}
	if len(cfg.Warnings) > 0 {
		t.Errorf("example config produced warnings: %v", cfg.Warnings)
	}
}
