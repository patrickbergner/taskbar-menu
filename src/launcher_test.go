//go:build windows

package main

import (
	"strings"
	"testing"
)

// A launcher with no id is unusable -- the id is the key --launch resolves and
// the AUMID is derived from -- so it is dropped with a warning, not kept.
func TestNormLaunchersRequiresID(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"launchers":[
		{"exec":"a.exe"},
		{"id":"good","exec":"b.exe"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Launchers) != 1 || cfg.Launchers[0].Id != "good" {
		t.Fatalf("got %+v, want only the id'd entry", cfg.Launchers)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "id") {
		t.Errorf("expected a warning about the missing id, got %v", cfg.Warnings)
	}
}

func TestNormLaunchersDropsTargetless(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"launchers":[
		{"id":"empty"},
		{"id":"ok","exec":"a.exe"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Launchers) != 1 || cfg.Launchers[0].Id != "ok" {
		t.Fatalf("targetless launcher should be dropped: %+v", cfg.Launchers)
	}
}

// appId and exec are the same thing said two ways; appId wins with a warning,
// exactly as it does for a menu Item.
func TestNormLaunchersExecAppIDConflict(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"launchers":[
		{"id":"both","exec":"a.exe","appId":"Some.App_x!App"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	l := cfg.Launchers[0]
	if l.Exec != "" || l.AppID != "Some.App_x!App" {
		t.Errorf("appId should win over exec: %+v", l)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "appId") {
		t.Errorf("expected a conflict warning, got %v", cfg.Warnings)
	}
}

func TestNormLaunchersShowNormalisation(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"launchers":[
		{"id":"max","exec":"a.exe","show":"MAXIMIZED"},
		{"id":"bogus","exec":"b.exe","show":"sideways"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Launchers[0].showCmd() != showMaximized {
		t.Errorf("MAXIMIZED not recognised for launcher")
	}
	if cfg.Launchers[1].showCmd() != showNormal {
		t.Errorf("bogus show should fall back to normal")
	}
}

func TestNormLaunchersExpandsEnv(t *testing.T) {
	t.Setenv("TBM_TEST_DIR", `C:\Data\Tools`)
	cfg, err := LoadConfig(writeConfig(t,
		`{"launchers":[{"id":"x","exec":"%TBM_TEST_DIR%\\a.exe","cwd":"%TBM_TEST_DIR%","args":["%TBM_TEST_DIR%\\p"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	l := cfg.Launchers[0]
	if l.Exec != `C:\Data\Tools\a.exe` || l.Cwd != `C:\Data\Tools` || l.Args[0] != `C:\Data\Tools\p` {
		t.Errorf("env not expanded in launcher: %+v", l)
	}
}

// Lookup prefers id, then falls back to label.
func TestFindLauncher(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"launchers":[
		{"id":"ff","label":"Firefox","exec":"ff.exe"},
		{"id":"tb","label":"Mail","exec":"tb.exe"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}

	if l, ok := findLauncher(cfg, "tb"); !ok || l.Exec != "tb.exe" {
		t.Errorf("id lookup failed: %+v ok=%v", l, ok)
	}
	if l, ok := findLauncher(cfg, "Firefox"); !ok || l.Id != "ff" {
		t.Errorf("label fallback failed: %+v ok=%v", l, ok)
	}
	if _, ok := findLauncher(cfg, "nope"); ok {
		t.Error("missing key should not resolve")
	}
}

// An id match must win even when it coincides with another entry's label.
func TestFindLauncherIDBeatsLabel(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"launchers":[
		{"id":"a","label":"shared","exec":"a.exe"},
		{"id":"shared","label":"b","exec":"b.exe"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := findLauncher(cfg, "shared"); !ok || l.Exec != "b.exe" {
		t.Errorf("id should win over a coincident label: %+v", l)
	}
}

func TestLauncherNode(t *testing.T) {
	l := Launcher{
		Id:       "x",
		Exec:     "a.exe",
		AppID:    "",
		Args:     []string{"-p", "v"},
		Cwd:      `C:\wd`,
		Elevated: true,
		Show:     "maximized",
	}
	n := launcherNode(l)
	if n.exec != "a.exe" || n.cwd != `C:\wd` || !n.elevated {
		t.Errorf("fields not carried into node: %+v", n)
	}
	if len(n.args) != 2 || n.args[1] != "v" {
		t.Errorf("args not carried: %+v", n.args)
	}
	if n.show != showMaximized {
		t.Errorf("show = %d, want %d", n.show, showMaximized)
	}
}
