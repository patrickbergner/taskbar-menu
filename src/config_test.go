//go:build windows

package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"items":[{"label":"A","exec":"a.exe"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IconSize != 16 {
		t.Errorf("IconSize = %d, want 16", cfg.IconSize)
	}
	if cfg.Theme != "auto" || cfg.Anchor != "taskbar" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.TrayIcon == nil || !*cfg.TrayIcon {
		t.Error("TrayIcon should default to true")
	}
	if got := cfg.Items[0].Show; got != "normal" {
		t.Errorf("Show = %q, want \"normal\"", got)
	}
	if cfg.Items[0].showCmd() != showNormal {
		t.Errorf("showCmd = %d, want %d", cfg.Items[0].showCmd(), showNormal)
	}
}

// trayIcon:false must be distinguishable from "not specified", which is why
// the field is a *bool.
func TestTrayIconExplicitFalse(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"trayIcon":false,"items":[{"label":"A","exec":"a.exe"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TrayIcon == nil || *cfg.TrayIcon {
		t.Error("explicit trayIcon:false was lost")
	}
}

// A syntax error must reach the caller with a line/column, since that is what
// gets rendered into the error menu item.
func TestLoadConfigSyntaxError(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, "{\n  \"items\": [\n    { \"label\": \"A\", }\n  ]\n}"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if !strings.Contains(err.Error(), "line ") {
		t.Errorf("error should name a line: %v", err)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected an error for a missing config")
	}
}

// Unknown "type" values are skipped rather than fatal. That is what keeps the
// schema forward-compatible with menuApp's special codes: a config written for
// a future build still opens on this one, minus the entries it cannot render.
func TestUnknownTypeIsSkippedNotFatal(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"items":[
		{"label":"Keep","exec":"a.exe"},
		{"label":"Tasks","type":"tasklist"},
		{"label":"Keep2","exec":"b.exe"}
	]}`))
	if err != nil {
		t.Fatalf("unknown type must not fail the load: %v", err)
	}
	if len(cfg.Items) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(cfg.Items), cfg.Items)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "tasklist") {
		t.Errorf("expected a warning naming the unknown type, got %v", cfg.Warnings)
	}
}

func TestInvalidItemsAreDropped(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"items":[
		{"label":"NoExec"},
		{"exec":"nolabel.exe"},
		{"label":"EmptySub","items":[]},
		{"label":"Good","exec":"good.exe"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Items) != 1 || cfg.Items[0].Label != "Good" {
		t.Fatalf("got %+v, want only the Good entry", cfg.Items)
	}
	if len(cfg.Warnings) != 3 {
		t.Errorf("got %d warnings, want 3: %v", len(cfg.Warnings), cfg.Warnings)
	}
}

// A Microsoft Store app is named by appId instead of exec, and that alone is
// enough to keep the entry -- it must not be dropped as "no exec".
func TestAppIDIsAcceptedWithoutExec(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"label":"Photos","appId":"Microsoft.Windows.Photos_8wekyb3d8bbwe!App"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Items) != 1 || cfg.Items[0].AppID != "Microsoft.Windows.Photos_8wekyb3d8bbwe!App" {
		t.Fatalf("appId entry not kept: %+v", cfg.Items)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cfg.Warnings)
	}
}

// exec and appId say the same thing two ways; giving both is a mistake, and
// appId wins with a warning rather than silently launching the exec.
func TestExecAndAppIDConflictPrefersAppID(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"label":"Both","exec":"a.exe","appId":"Some.App_x!App"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Items) != 1 {
		t.Fatalf("entry dropped: %+v", cfg.Items)
	}
	if cfg.Items[0].Exec != "" || cfg.Items[0].AppID != "Some.App_x!App" {
		t.Errorf("appId should win over exec: %+v", cfg.Items[0])
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "appId") {
		t.Errorf("expected a warning about the conflict, got %v", cfg.Warnings)
	}
}

func TestSeparatorsSurviveNormalisation(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"items":[
		{"label":"A","exec":"a.exe"},
		{"type":"separator"},
		{"label":"B","exec":"b.exe"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Items) != 3 || cfg.Items[1].Type != "separator" {
		t.Fatalf("separator lost: %+v", cfg.Items)
	}
	// A separator needs neither a label nor an exec.
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cfg.Warnings)
	}
}

func TestNestedSubmenus(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"items":[
		{"label":"L1","items":[
			{"label":"L2","items":[
				{"label":"Leaf","exec":"deep.exe"}
			]},
			{"type":"separator"},
			{"label":"Sibling","exec":"s.exe"}
		]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	l1 := cfg.Items[0]
	if len(l1.Items) != 3 {
		t.Fatalf("L1 has %d children, want 3", len(l1.Items))
	}
	if l1.Items[0].Items[0].Exec != "deep.exe" {
		t.Errorf("nesting lost at depth 3: %+v", l1.Items[0])
	}
}

func TestShowAndEnumNormalisation(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{
		"theme":"DARK","anchor":"Cursor","iconSize":999,
		"items":[
			{"label":"Max","exec":"a.exe","show":"MAXIMIZED"},
			{"label":"Min","exec":"b.exe","show":"minimized"},
			{"label":"Bogus","exec":"c.exe","show":"sideways"}
		]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "dark" || cfg.Anchor != "cursor" {
		t.Errorf("enums not lowercased: %+v", cfg)
	}
	if cfg.IconSize != 64 {
		t.Errorf("IconSize = %d, want clamped to 64", cfg.IconSize)
	}
	if cfg.Items[0].showCmd() != showMaximized {
		t.Errorf("MAXIMIZED not recognised")
	}
	if cfg.Items[1].showCmd() != showMinimized {
		t.Errorf("minimized not recognised")
	}
	if cfg.Items[2].showCmd() != showNormal {
		t.Errorf("bogus show should fall back to normal")
	}
}

func TestBadEnumFallsBackWithWarning(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"theme":"neon","items":[{"label":"A","exec":"a.exe"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "auto" {
		t.Errorf("Theme = %q, want fallback to \"auto\"", cfg.Theme)
	}
	if len(cfg.Warnings) == 0 {
		t.Error("expected a warning about the unknown theme")
	}
}

func TestExpandEnv(t *testing.T) {
	t.Setenv("TBM_TEST_DIR", `C:\Data\Tools`)
	cases := []struct{ in, want string }{
		{`%TBM_TEST_DIR%\app.exe`, `C:\Data\Tools\app.exe`},
		{`no vars here`, `no vars here`},
		{`%TBM_NOT_SET%\x`, `%TBM_NOT_SET%\x`}, // left visible so a typo is obvious
		{`100%% sure`, `100% sure`},
		{`trailing %`, `trailing %`},
		{``, ``},
	}
	for _, c := range cases {
		if got := expandEnv(c.in); got != c.want {
			t.Errorf("expandEnv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExpandEnvAppliedToConfig(t *testing.T) {
	t.Setenv("TBM_TEST_DIR", `C:\Data\Tools`)
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"label":"A","exec":"%TBM_TEST_DIR%\\a.exe","cwd":"%TBM_TEST_DIR%","args":["%TBM_TEST_DIR%\\p"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	it := cfg.Items[0]
	if it.Exec != `C:\Data\Tools\a.exe` {
		t.Errorf("exec = %q", it.Exec)
	}
	if it.Cwd != `C:\Data\Tools` {
		t.Errorf("cwd = %q", it.Cwd)
	}
	if it.Args[0] != `C:\Data\Tools\p` {
		t.Errorf("args[0] = %q", it.Args[0])
	}
}

// Notepad and PowerShell's Set-Content write a UTF-8 BOM by default, and
// encoding/json rejects one outright. Since the whole point of hot reload is
// that the file gets edited in whatever is at hand, the loader has to cope.
func TestLoadConfigToleratesBOMs(t *testing.T) {
	body := `{"items":[{"label":"A","exec":"a.exe"}]}`

	utf16le := func(s string, order binary.ByteOrder) []byte {
		out := []byte{}
		if order == binary.LittleEndian {
			out = append(out, 0xFF, 0xFE)
		} else {
			out = append(out, 0xFE, 0xFF)
		}
		for _, u := range utf16.Encode([]rune(s)) {
			b := make([]byte, 2)
			order.PutUint16(b, u)
			out = append(out, b...)
		}
		return out
	}

	cases := []struct {
		name string
		raw  []byte
	}{
		{"no BOM", []byte(body)},
		{"UTF-8 BOM", append([]byte{0xEF, 0xBB, 0xBF}, body...)},
		{"UTF-16 LE BOM", utf16le(body, binary.LittleEndian)},
		{"UTF-16 BE BOM", utf16le(body, binary.BigEndian)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, c.raw, 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if len(cfg.Items) != 1 || cfg.Items[0].Label != "A" {
				t.Errorf("got %+v", cfg.Items)
			}
		})
	}
}

// Non-ASCII labels and paths have to survive the round trip: one of the live
// entries points at a directory containing "Straße".
func TestLoadConfigHandlesNonASCII(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := "{\"items\":[{\"label\":\"Heizung\",\"exec\":\"C:\\\\x\\\\Heinrich-Budde-Straße 22\\\\Heizung.xlsx\"}]}"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `C:\x\Heinrich-Budde-Straße 22\Heizung.xlsx`
	if cfg.Items[0].Exec != want {
		t.Errorf("exec = %q, want %q", cfg.Items[0].Exec, want)
	}
}

// Hot reload hinges entirely on this comparison.
func TestStampDetectsChange(t *testing.T) {
	path := writeConfig(t, `{"items":[]}`)

	first := statStamp(path)
	if !first.ok {
		t.Fatal("stamp should be ok for an existing file")
	}
	if !first.equal(statStamp(path)) {
		t.Error("stamp of an unchanged file must compare equal")
	}

	// A size change is caught regardless of filesystem timestamp resolution.
	if err := os.WriteFile(path, []byte(`{"items":[{"label":"A","exec":"a.exe"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if first.equal(statStamp(path)) {
		t.Error("stamp must differ after the file changed")
	}

	missing := statStamp(filepath.Join(t.TempDir(), "gone.json"))
	if missing.ok {
		t.Error("stamp of a missing file must not be ok")
	}
	if missing.equal(first) {
		t.Error("a missing file must not compare equal to an existing one")
	}
}
