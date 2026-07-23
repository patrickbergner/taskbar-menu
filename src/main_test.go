//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		o, err := parseArgs(nil)
		if err != nil {
			t.Fatal(err)
		}
		if o.cfgPath != "" || o.background {
			t.Errorf("unexpected defaults: %+v", o)
		}
	})

	t.Run("config and background", func(t *testing.T) {
		o, err := parseArgs([]string{"--config", `C:\x\my.json`, "--background"})
		if err != nil {
			t.Fatal(err)
		}
		if o.cfgPath != `C:\x\my.json` {
			t.Errorf("cfgPath = %q", o.cfgPath)
		}
		if !o.background {
			t.Error("background not set")
		}
	})

	t.Run("short forms", func(t *testing.T) {
		o, err := parseArgs([]string{"-c", "a.json", "-b"})
		if err != nil || o.cfgPath != "a.json" || !o.background {
			t.Errorf("o = %+v, err = %v", o, err)
		}
	})

	t.Run("icon helpers", func(t *testing.T) {
		o, err := parseArgs([]string{"--list-icons", `C:\a.dll`})
		if err != nil || o.listIcons != `C:\a.dll` {
			t.Errorf("o = %+v, err = %v", o, err)
		}
		o, err = parseArgs([]string{"--pick-icon", `C:\a.dll`})
		if err != nil || o.pickIcon != `C:\a.dll` {
			t.Errorf("o = %+v, err = %v", o, err)
		}
	})

	t.Run("missing value", func(t *testing.T) {
		if _, err := parseArgs([]string{"--config"}); err == nil {
			t.Error("expected an error when --config has no value")
		}
	})

	t.Run("unknown flag", func(t *testing.T) {
		if _, err := parseArgs([]string{"--nope"}); err == nil {
			t.Error("expected an error for an unknown flag")
		}
	})
}

// The cursor position travels from the launching process to the resident
// server packed into a WPARAM/LPARAM pair. Monitors placed left of or above the
// primary have negative coordinates, so the packing has to be sign-preserving.
func TestCursorPackingRoundTrip(t *testing.T) {
	cases := []POINT{
		{0, 0},
		{640, 1420},
		{2559, 1439},
		{-1200, 1000},  // monitor to the left of primary
		{100, -800},    // monitor above primary
		{-1920, -1080}, // both
	}
	for _, pt := range cases {
		wp := uintptr(uint32(pt.X))
		lp := uintptr(uint32(pt.Y))
		got := POINT{X: int32(uint32(wp)), Y: int32(uint32(lp))}
		if got != pt {
			t.Errorf("packing round trip: %+v -> %+v", pt, got)
		}
	}
}

// The flat slice is what WM_DRAWITEM uses to find its node, via dwItemData.
// Zero must stay reserved for "not one of ours", because menus we did not
// build deliver dwItemData == 0.
func TestNodeDataIndexing(t *testing.T) {
	a := &appState{byID: map[uint32]*node{}}
	n1 := &node{label: "one"}
	n2 := &node{label: "two"}
	a.register(n1)
	a.register(n2)

	if n1.data == 0 || n2.data == 0 {
		t.Fatal("dwItemData must never be zero for our own items")
	}
	if a.nodeFromData(n1.data) != n1 || a.nodeFromData(n2.data) != n2 {
		t.Error("nodeFromData did not round trip")
	}
	if a.nodeFromData(0) != nil {
		t.Error("dwItemData 0 must resolve to nil")
	}
	if a.nodeFromData(uintptr(len(a.flat))+1) != nil {
		t.Error("out-of-range dwItemData must resolve to nil")
	}
}

// Command ids must be unique across the main menu, the tray menu and the error
// menu, since they all share one id space and one byID map.
func TestBuildNodesAssignsUniqueIDs(t *testing.T) {
	a := &appState{byID: map[uint32]*node{}, cfg: defaultConfig()}
	items := []Item{
		{Label: "A", Type: "item", Exec: "a.exe"},
		{Type: "separator"},
		{Label: "Sub", Type: "item", Items: []Item{
			{Label: "B", Type: "item", Exec: "b.exe"},
			{Label: "C", Type: "item", Exec: "c.exe"},
		}},
	}
	nodes := a.buildNodes(items)
	tray := a.trayNodes()

	if len(nodes) != 3 {
		t.Fatalf("got %d top-level nodes, want 3", len(nodes))
	}
	if !nodes[1].separator {
		t.Error("second node should be a separator")
	}
	if len(nodes[2].children) != 2 {
		t.Fatalf("submenu has %d children, want 2", len(nodes[2].children))
	}
	// A submenu parent is opened by the system, not by a command id.
	if nodes[2].id != 0 {
		t.Error("submenu parent should not consume a command id")
	}
	if nodes[1].id != 0 {
		t.Error("separator should not consume a command id")
	}

	seen := map[uint32]bool{}
	for id, n := range a.byID {
		if id == 0 {
			t.Error("id 0 must not be handed out: TPM_RETURNCMD uses it for \"nothing selected\"")
		}
		if seen[id] {
			t.Errorf("duplicate command id %d", id)
		}
		seen[id] = true
		if n == nil {
			t.Errorf("id %d maps to nil", id)
		}
	}
	// A, B and C from the config, plus every tray entry that is not a separator.
	want := 3 + (len(tray) - 2)
	if len(seen) != want {
		t.Errorf("registered %d ids, want %d", len(seen), want)
	}
}

func TestInstanceNames(t *testing.T) {
	classA, mutexA := instanceNames(`C:\App\config.json`)

	// Same file spelled differently (case, redundant path element) must map to
	// the same instance -- otherwise two servers would fight over one config.
	classB, mutexB := instanceNames(`c:\app\.\CONFIG.JSON`)
	if classA != classB || mutexA != mutexB {
		t.Errorf("same config gave different names:\n  %s / %s\n  %s / %s",
			classA, mutexA, classB, mutexB)
	}

	// A different config must get its own class and mutex so it starts its own
	// server instead of hijacking the first.
	classC, mutexC := instanceNames(`C:\App\other.json`)
	if classC == classA || mutexC == mutexA {
		t.Errorf("distinct configs collided: %s / %s", classC, mutexC)
	}

	// The names are used verbatim as a window class and a kernel object name.
	if classA == classPrefix || !strings.HasPrefix(classA, classPrefix+".") {
		t.Errorf("class name %q not suffixed from prefix", classA)
	}
	if !strings.HasPrefix(mutexA, mutexPrefix+".") {
		t.Errorf("mutex name %q not suffixed from prefix", mutexA)
	}
}
