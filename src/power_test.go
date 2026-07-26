//go:build windows

package main

import "testing"

// NOTHING IN THIS FILE MAY CALL THE REAL POWER APIS. Every test substitutes the
// powerExec seam, so the machine never signs out, sleeps or reboots. If you add
// a test here, substitute the seam first.
func withFakePower(t *testing.T) *[]powerOp {
	t.Helper()
	var got []powerOp
	saved := powerExec
	powerExec = func(op powerOp) error {
		got = append(got, op)
		return nil
	}
	t.Cleanup(func() { powerExec = saved })
	return &got
}

func TestPowerOpForCoversEveryActionSpecial(t *testing.T) {
	for _, e := range specials {
		if e.kind != kindAction {
			continue
		}
		if _, ok := powerOpFor(e.id); !ok {
			t.Errorf("catalog has action %q with no powerOp -- it would build a nil action", e.id)
		}
	}
}

// The destructive set is what decides the default, so it is asserted rather than
// left to the reader of a switch.
func TestConfirmDefaults(t *testing.T) {
	cases := map[string]bool{
		"signOut": true, "restart": true, "shutdown": true,
		"lock": false, "sleep": false, "hibernate": false,
	}
	for id, want := range cases {
		if got := confirmDefault(id); got != want {
			t.Errorf("confirmDefault(%q) = %v, want %v", id, got, want)
		}
	}
	if confirmDefault("deviceManager") {
		t.Error("a non-power id must never default to confirming")
	}
}

func TestNormalisationFillsConfirmForPowerSpecials(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"special":"shutdown"},{"special":"lock"},{"special":"shutdown","confirm":false},{"special":"lock","confirm":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, false, true}
	if len(cfg.Items) != len(want) {
		t.Fatalf("got %d items, want %d (warnings %v)", len(cfg.Items), len(want), cfg.Warnings)
	}
	for i, w := range want {
		if cfg.Items[i].Confirm == nil {
			t.Errorf("item %d: Confirm not filled in", i)
			continue
		}
		if got := cfg.Items[i].confirms(); got != w {
			t.Errorf("item %d (%s): confirms = %v, want %v", i, cfg.Items[i].Special, got, w)
		}
	}
}

// A power special has no exec, so it must survive the "no target" drop that
// every other targetless entry hits.
func TestPowerSpecialSurvivesNormalisation(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"items":[{"special":"shutdown"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Items) != 1 {
		t.Fatalf("power special was dropped: %v", cfg.Warnings)
	}
	it := cfg.Items[0]
	if it.Special != "shutdown" || it.Exec != "" || it.Label != "Shut Down" || it.Icon == "" {
		t.Errorf("unexpected normalisation result: %+v", it)
	}
}

func TestConfirmOnNonPowerEntryWarns(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"items":[{"label":"A","exec":"a.exe","confirm":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Items) != 1 {
		t.Fatal("the entry should survive; confirm is merely useless there")
	}
	if len(cfg.Warnings) != 1 {
		t.Errorf("warnings = %v, want exactly one", cfg.Warnings)
	}
}

// The gating itself: confirm=false must act, and a "No" must not.
func TestPowerActionRunsWhenNotConfirming(t *testing.T) {
	got := withFakePower(t)
	var a appState
	act := a.powerAction("shutdown", false)
	if act == nil {
		t.Fatal("no action built")
	}
	act()
	if len(*got) != 1 || (*got)[0] != opShutdown {
		t.Errorf("ops = %v, want exactly [opShutdown]", *got)
	}
}

func TestPowerActionSelectsTheRightOp(t *testing.T) {
	got := withFakePower(t)
	var a appState
	for _, id := range []string{"lock", "signOut", "restart", "shutdown", "sleep", "hibernate"} {
		a.powerAction(id, false)()
	}
	want := []powerOp{opLock, opSignOut, opRestart, opShutdown, opSleep, opHibernate}
	if len(*got) != len(want) {
		t.Fatalf("ops = %v, want %v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Errorf("op %d = %v, want %v", i, (*got)[i], want[i])
		}
	}
}

func TestPowerActionUnknownIdIsNil(t *testing.T) {
	var a appState
	if a.powerAction("deviceManager", false) != nil {
		t.Error("a non-power id must not produce an action")
	}
}

// The launcher shim path: --launch shutdown must carry the built-in rather than
// try to ShellExecuteEx an empty target.
func TestLauncherNodeCarriesPowerAction(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"launchers":[{"id":"off","special":"shutdown","confirm":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Launchers) != 1 {
		t.Fatalf("launcher dropped: %v", cfg.Warnings)
	}
	got := withFakePower(t)
	n := launcherNode(cfg.Launchers[0])
	if n.action == nil {
		t.Fatal("launcherNode did not attach the built-in")
	}
	if n.exec != "" {
		t.Errorf("exec = %q, want empty for a built-in", n.exec)
	}
	n.action()
	if len(*got) != 1 || (*got)[0] != opShutdown {
		t.Errorf("ops = %v, want [opShutdown]", *got)
	}
}

// Live system, and harmless: enabling a privilege changes nothing on its own.
// Skipped rather than failed for an account that does not hold it.
func TestEnablePrivilegeShutdown(t *testing.T) {
	if err := enablePrivilege(seShutdownName); err != nil {
		t.Skipf("SeShutdownPrivilege not available here: %v", err)
	}
}
