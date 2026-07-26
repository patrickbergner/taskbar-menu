//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newTestApp is the minimum appState the node builders need: byID must exist
// before leafNode can register a command id in it.
func newTestApp() *appState {
	return &appState{
		byID:     map[uint32]*node{},
		dynMenus: map[syscall.Handle]*node{},
	}
}

func labels(nodes []*node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.separator {
			out = append(out, "----")
			continue
		}
		out = append(out, n.label)
	}
	return out
}

func TestDisplayNameStripsShortcutExtension(t *testing.T) {
	cases := []struct {
		name  string
		isDir bool
		want  string
	}{
		{"Firefox.lnk", false, "Firefox"},
		{"Firefox.LNK", false, "Firefox"},
		{"Bookmark.url", false, "Bookmark"},
		{"notes.txt", false, "notes.txt"},
		{"app.exe", false, "app.exe"},
		{"my.lnk.txt", false, "my.lnk.txt"}, // only a trailing .lnk counts
		{"Accessories", true, "Accessories"},
		{"folder.lnk", true, "folder.lnk"}, // a directory keeps its name
	}
	for _, c := range cases {
		if got := displayName(c.name, c.isDir); got != c.want {
			t.Errorf("displayName(%q, %v) = %q, want %q", c.name, c.isDir, got, c.want)
		}
	}
}

// desktop.ini renaming, built from scratch so the test says what it depends on.
// The shell only honours a customized folder, which means the marker attributes
// Explorer itself sets: hidden+system on the ini, and system (or read-only) on
// the folder. Without them SHGetLocalizedName reports E_INVALIDARG and the file
// names would stand -- which is also the proof that a folder without a
// desktop.ini pays nothing.
func TestLocalizedNameFollowsDesktopIni(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "foo.lnk")
	mustWrite(t, file, "x")
	mustWrite(t, filepath.Join(dir, desktopIni),
		"[.ShellClassInfo]\r\n"+
			`LocalizedResourceName=@%SystemRoot%\system32\shell32.dll,-21770`+"\r\n"+
			"[LocalizedFileNames]\r\n"+
			`foo.lnk=@%SystemRoot%\system32\filemgmt.dll,-2204`+"\r\n")
	markCustomized(t, dir)
	defer purgeLocalizedNames()

	// The two resources are Documents and Services, whatever this machine's
	// language calls them, so the assertion is that the name changed at all --
	// and that it did not come back as the file name or a resource spec.
	name := localizedName(file)
	if name == "" {
		t.Skip("the shell does not honour desktop.ini here")
	}
	if strings.EqualFold(name, "foo.lnk") || strings.HasPrefix(name, "@") {
		t.Errorf("localizedName = %q, want the string resource behind it", name)
	}
	if folder := localizedName(dir); folder == "" || folder == filepath.Base(dir) {
		t.Errorf("localizedName(folder) = %q, want its LocalizedResourceName", folder)
	}

	// And the listing must use it: this is the bug as reported, where Windows
	// Tools showed "dfrgui" and "services" instead of the names Explorer shows.
	a := newTestApp()
	got := labels(a.expandFolder([]string{dir}, 1, defaultLimit, newBudget()))
	if len(got) != 1 || got[0] != name {
		t.Errorf("listing = %v, want [%s]", got, name)
	}
}

// A folder with nothing to say about its contents must leave every name alone.
func TestPlainFolderKeepsFileNames(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "services.lnk"), "x")
	defer purgeLocalizedNames()

	a := newTestApp()
	got := labels(a.expandFolder([]string{dir}, 1, defaultLimit, newBudget()))
	if len(got) != 1 || got[0] != "services" {
		t.Errorf("listing = %v, want [services]", got)
	}
}

// markCustomized applies the attributes the shell requires before it will read
// a desktop.ini at all.
func markCustomized(t *testing.T, dir string) {
	t.Helper()
	set := func(path string, attrs uint32) {
		p, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.SetFileAttributes(p, attrs); err != nil {
			t.Fatal(err)
		}
	}
	set(filepath.Join(dir, desktopIni), syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_SYSTEM)
	set(dir, syscall.FILE_ATTRIBUTE_DIRECTORY|syscall.FILE_ATTRIBUTE_SYSTEM)
	// t.TempDir's cleanup cannot remove a system directory, so put it back.
	t.Cleanup(func() { set(dir, syscall.FILE_ATTRIBUTE_DIRECTORY) })
}

func TestSortDirEntriesDirsFirstThenAlpha(t *testing.T) {
	in := []dirEntry{
		{label: "zebra.txt", path: `C:\z`},
		{label: "Apple", path: `C:\Apple`, dir: true},
		{label: "banana.txt", path: `C:\b`},
		{label: "apricot", path: `C:\apricot`, dir: true},
		{label: "Banana.txt", path: `C:\B`},
	}
	sortDirEntries(in)
	got := make([]string, len(in))
	for i, e := range in {
		got[i] = e.label
	}
	want := []string{"Apple", "apricot", "banana.txt", "Banana.txt", "zebra.txt"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted = %v, want %v", got, want)
		}
	}
}

// The Start Menu rule: the per-user and all-users trees must read as one menu.
func TestMergeLevelsUnionsDirectoriesAndDedupesFiles(t *testing.T) {
	user := []dirEntry{
		{label: "Accessories", path: `C:\u\Accessories`, dir: true, roots: []string{`C:\u\Accessories`}},
		{label: "Firefox", path: `C:\u\Firefox.lnk`},
	}
	all := []dirEntry{
		{label: "accessories", path: `C:\a\Accessories`, dir: true, roots: []string{`C:\a\Accessories`}},
		{label: "Firefox", path: `C:\a\Firefox.lnk`},
		{label: "Notepad", path: `C:\a\Notepad.lnk`},
	}
	got := mergeLevels([][]dirEntry{user, all})

	if len(got) != 3 {
		t.Fatalf("got %d entries (%v), want 3", len(got), labelsOf(got))
	}
	acc := got[0]
	if !acc.dir || len(acc.roots) != 2 {
		t.Errorf("Accessories should merge both roots, got %+v", acc)
	}
	if acc.roots[0] != `C:\u\Accessories` || acc.roots[1] != `C:\a\Accessories` {
		t.Errorf("root order lost: %v", acc.roots)
	}
	// The duplicate file keeps the per-user copy, which is the editable one.
	if got[1].label != "Firefox" || got[1].path != `C:\u\Firefox.lnk` {
		t.Errorf("de-dup should keep the first (per-user) file, got %+v", got[1])
	}
}

// The real Start Menu ships an "Administrative Tools" folder next to an
// "Administrative Tools.lnk" that points at it; showing both is confusing.
func TestMergeLevelsDropsFilesShadowedByADirectory(t *testing.T) {
	in := []dirEntry{
		{label: "Administrative Tools", path: `C:\u\Administrative Tools`, dir: true,
			roots: []string{`C:\u\Administrative Tools`}},
		{label: "Administrative Tools", path: `C:\u\Administrative Tools.lnk`},
		{label: "Keeper", path: `C:\u\Keeper.lnk`},
	}
	got := mergeLevels([][]dirEntry{in})
	if len(got) != 2 {
		t.Fatalf("got %v, want the folder and Keeper only", labelsOf(got))
	}
	if !got[0].dir {
		t.Error("the surviving Administrative Tools entry should be the directory")
	}
	if got[1].label != "Keeper" {
		t.Errorf("unrelated files must survive, got %v", labelsOf(got))
	}
}

func labelsOf(e []dirEntry) []string {
	out := make([]string, len(e))
	for i := range e {
		out[i] = e[i].label
	}
	return out
}

func TestClampDepthAndLimit(t *testing.T) {
	for in, want := range map[int]int{0: defaultDepth, -3: defaultDepth, 1: 1, 5: 5, 99: maxDepth} {
		if got := clampDepth(in); got != want {
			t.Errorf("clampDepth(%d) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[int]int{0: defaultLimit, -1: defaultLimit, 10: 10, 99999: maxLimit} {
		if got := clampLimit(in); got != want {
			t.Errorf("clampLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestExpandFolderListsAndSorts(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "zeta.txt"), "z")
	mustWrite(t, filepath.Join(dir, "Alpha.lnk"), "a")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	a := newTestApp()
	got := labels(a.expandFolder([]string{dir}, 1, defaultLimit, newBudget()))
	want := []string{"sub", "Alpha", "zeta.txt"} // dir first, then .lnk stripped, alphabetical
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// At depth 1 a subdirectory is a plain leaf that opens in Explorer; at depth 2
// it becomes a submenu of its own, and crucially still has no children until it
// is opened.
func TestExpandFolderRespectsDepth(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "sub", "inner.txt"), "i")

	a := newTestApp()
	flat := a.expandFolder([]string{dir}, 1, defaultLimit, newBudget())
	if len(flat) != 1 || flat[0].dyn != nil || !strings.HasSuffix(flat[0].exec, "sub") {
		t.Fatalf("depth 1 should make a plain leaf, got %+v", flat[0])
	}

	a = newTestApp()
	deep := a.expandFolder([]string{dir}, 2, defaultLimit, newBudget())
	if len(deep) != 1 || deep[0].dyn == nil {
		t.Fatalf("depth 2 should make a dynamic submenu, got %+v", deep[0])
	}
	if len(deep[0].children) != 0 {
		t.Error("a dynamic submenu must have no children before it is opened")
	}
	if !deep[0].hasSubmenu() {
		t.Error("a dynamic node must report hasSubmenu so it measures room for the arrow")
	}
}

func TestExpandFolderSkipsHiddenAndDesktopIni(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "visible.txt"), "v")
	mustWrite(t, filepath.Join(dir, "desktop.ini"), "[.ShellClassInfo]")
	hidden := filepath.Join(dir, "hidden.txt")
	mustWrite(t, hidden, "h")
	if p, err := syscall.UTF16PtrFromString(hidden); err == nil {
		_ = syscall.SetFileAttributes(p, syscall.FILE_ATTRIBUTE_HIDDEN)
	}

	a := newTestApp()
	got := labels(a.expandFolder([]string{dir}, 1, defaultLimit, newBudget()))
	for _, l := range got {
		if l == "desktop.ini" || l == "hidden.txt" {
			t.Errorf("%q should not appear; got %v", l, got)
		}
	}
	if len(got) != 1 || got[0] != "visible.txt" {
		t.Errorf("got %v, want [visible.txt]", got)
	}
}

func TestTruncationEmitsMoreNode(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		mustWrite(t, filepath.Join(dir, n), "x")
	}
	a := newTestApp()
	got := a.expandFolder([]string{dir}, 1, 2, newBudget())
	if len(got) != 3 {
		t.Fatalf("got %v, want 2 entries plus the overflow node", labels(got))
	}
	more := got[2]
	if !strings.HasPrefix(more.label, "More…") {
		t.Errorf("last entry = %q, want the overflow node", more.label)
	}
	// The overflow must be a door, not a dead end.
	if more.disabled || more.id == 0 || more.exec != dir {
		t.Errorf("overflow node should open the folder: %+v", more)
	}
}

func TestWalkBudgetStopsWhenSpent(t *testing.T) {
	b := &walkBudget{nodes: 10, deadline: time.Now().Add(time.Minute)}
	if b.spent() {
		t.Fatal("fresh budget reports spent")
	}
	b.take(10)
	if !b.spent() {
		t.Error("budget should be spent after taking all its nodes")
	}

	expired := &walkBudget{nodes: 1000, deadline: time.Now().Add(-time.Second)}
	if !expired.spent() {
		t.Error("a passed deadline must count as spent")
	}

	a := newTestApp()
	if got := a.expandFolder([]string{t.TempDir()}, 1, defaultLimit, expired); got != nil {
		t.Errorf("an exhausted budget must stop the walk, got %v", labels(got))
	}
}

func TestExpandEmptyFolderIsMarked(t *testing.T) {
	a := newTestApp()
	got := a.expand(&dynSource{kind: dynFolder, roots: []string{t.TempDir()}, depth: 1, limit: defaultLimit})
	if len(got) != 1 || got[0].label != ui.EmptyFolder || !got[0].disabled {
		t.Errorf("an empty folder should say so, got %v", labels(got))
	}
}

func TestExpandLeadsWithOpenItem(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "a")
	a := newTestApp()
	got := a.expand(&dynSource{
		kind: dynFolder, roots: []string{dir}, depth: 1, limit: defaultLimit, openLabel: "Repos",
	})
	if len(got) < 3 || got[0].label != "Open Repos" || !got[1].separator {
		t.Fatalf("want [Open Repos, ----, ...], got %v", labels(got))
	}
	if got[0].exec != dir {
		t.Errorf("the Open item should target the root, got %q", got[0].exec)
	}
}

// A missing root among several is normal, not an error: %PUBLIC%\Desktop and the
// per-user Start Menu tree are both routinely absent.
func TestExpandFolderToleratesMissingRoot(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "a")
	a := newTestApp()
	got := labels(a.expandFolder([]string{filepath.Join(dir, "nope"), dir}, 1, defaultLimit, newBudget()))
	if len(got) != 1 || got[0] != "a.txt" {
		t.Errorf("got %v, want [a.txt]", got)
	}
}

func TestDynSourceForFolderAndSpecial(t *testing.T) {
	if d := dynSourceFor(Item{Folder: `C:\X`, Depth: 2, Limit: 5, Label: "X"}); d == nil {
		t.Fatal("a folder item must produce a source")
	} else if d.depth != 2 || d.limit != 5 || d.openLabel != "X" || d.roots[0] != `C:\X` {
		t.Errorf("unexpected source %+v", d)
	}

	d := dynSourceFor(Item{Special: "startMenu", Depth: 1, Limit: defaultLimit})
	if d == nil {
		t.Fatal("startMenu must produce a source")
	}
	if len(d.roots) != 2 {
		t.Errorf("startMenu should merge two roots, got %v", d.roots)
	}
	for _, r := range d.roots {
		if strings.Contains(r, "%") {
			t.Errorf("root %q still holds an unexpanded %%VAR%%", r)
		}
	}

	if dynSourceFor(Item{Exec: "a.exe"}) != nil {
		t.Error("a plain exec item is not dynamic")
	}
	if dynSourceFor(Item{Special: "taskManager"}) != nil {
		t.Error("a kindExec special is not dynamic")
	}
}

func TestFolderConfigDefaultsAndFlags(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"folder":"C:\\Data\\Repos"},{"label":"S","special":"startMenu"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HasDynamic {
		t.Error("HasDynamic must be set, or the menu will be cached and go stale")
	}
	if cfg.Items[0].Label != "Repos" {
		t.Errorf("folder label = %q, want the base name %q", cfg.Items[0].Label, "Repos")
	}
	// The defaults are applied where they are read, not written back onto the
	// Item -- a plain entry has no business carrying a depth.
	d := dynSourceFor(cfg.Items[0])
	if d == nil {
		t.Fatal("a folder item must produce a source")
	}
	if d.depth != defaultDepth || d.limit != defaultLimit {
		t.Errorf("defaults not applied: depth=%d limit=%d", d.depth, d.limit)
	}
}

// depth on an entry that lists nothing is a silent no-op unless it warns, and
// this file warns about every other ignored key.
func TestDepthAndConfirmWarnOnTheWrongKind(t *testing.T) {
	cases := map[string]string{
		`{"items":[{"label":"A","exec":"a.exe","depth":3}]}`: "depth/limit",
		`{"items":[{"special":"allSettings","depth":3}]}`:    "depth/limit",
		`{"items":[{"special":"shutdown","limit":50}]}`:      "depth/limit",
		`{"items":[{"special":"startMenu","confirm":true}]}`: "confirm",
	}
	for body, want := range cases {
		cfg, err := LoadConfig(writeConfig(t, body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], want) {
			t.Errorf("%s\n  warnings = %v, want one mentioning %q", body, cfg.Warnings, want)
		}
	}

	// ...and must NOT warn where the key does apply.
	for _, body := range []string{
		`{"items":[{"special":"startMenu","depth":2}]}`,
		`{"items":[{"folder":"C:\\X","limit":10}]}`,
		`{"items":[{"special":"shutdown","confirm":false}]}`,
	} {
		cfg, err := LoadConfig(writeConfig(t, body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if len(cfg.Warnings) != 0 {
			t.Errorf("%s\n  unexpected warnings: %v", body, cfg.Warnings)
		}
	}
}

func TestStaticConfigIsNotDynamic(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		`{"items":[{"special":"taskManager"},{"label":"A","exec":"a.exe"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HasDynamic {
		t.Error("a config with no dynamic entries must keep the cached-menu fast path")
	}
}

func TestSettingsListWellFormed(t *testing.T) {
	if len(settingsPages) < 20 {
		t.Fatalf("only %d settings pages; the list is meant to be worth browsing", len(settingsPages))
	}
	seen := map[string]bool{}
	lastGroup := ""
	groupSeen := map[string]bool{}
	for _, s := range settingsPages {
		if s.group == "" {
			t.Errorf("incomplete settings page: %+v", s)
		}
		if !strings.HasPrefix(s.uri, "ms-settings:") {
			t.Errorf("uri %q must start with ms-settings:", s.uri)
		}
		if seen[s.uri] {
			t.Errorf("duplicate uri %q", s.uri)
		}
		seen[s.uri] = true

		// expandSettings starts a new submenu whenever the group changes, so a
		// group that reappears later would silently split into two submenus.
		if s.group != lastGroup {
			if groupSeen[s.group] {
				t.Errorf("group %q is not contiguous; it would split into two submenus", s.group)
			}
			groupSeen[s.group] = true
			lastGroup = s.group
		}
	}
}

func TestExpandSettingsBuildsGroupedTree(t *testing.T) {
	a := newTestApp()
	got := a.expandSettings()
	if len(got) < 5 {
		t.Fatalf("got %d groups, want one per settings group", len(got))
	}
	total := 0
	for _, g := range got {
		if len(g.children) == 0 {
			t.Errorf("group %q has no pages", g.label)
		}
		if !g.hasSubmenu() {
			t.Errorf("group %q must report hasSubmenu", g.label)
		}
		total += len(g.children)
	}
	if total != len(settingsPages) {
		t.Errorf("tree holds %d pages, want %d", total, len(settingsPages))
	}
}

func TestIsDriveRoot(t *testing.T) {
	cases := map[string]bool{
		`C:\`: true, `C:`: true, `z:\`: true,
		`C:\Users`: false, `shell:AppsFolder`: false, ``: false, `CC:\`: false,
	}
	for in, want := range cases {
		if got := isDriveRoot(in); got != want {
			t.Errorf("isDriveRoot(%q) = %v, want %v", in, got, want)
		}
	}
}

// The names being converted are parent-relative, so every shape the three
// listings produce has to come out absolute. The two GUID-ish rows are the
// regression: a desktop app in the AppsFolder gets a known-folder-relative
// AUMID, and a Control Panel applet a "::{CLSID}", and both used to be handed
// back untouched -- which drew the generic icon and, on click, failed with "the
// system cannot find the file specified".
func TestLaunchTargetFor(t *testing.T) {
	cases := map[string]string{
		// Packaged app: a bare AUMID.
		`Microsoft.WindowsCalculator_8wekyb3d8bbwe!App`: appsFolderPrefix + `Microsoft.WindowsCalculator_8wekyb3d8bbwe!App`,
		// Desktop app in the AppsFolder: known-folder-relative, backslashes and all.
		`{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\charmap.exe`:   appsFolderPrefix + `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\charmap.exe`,
		`{6D809377-6AF0-444B-8957-A3773F02200E}\App\thing.exe`: appsFolderPrefix + `{6D809377-6AF0-444B-8957-A3773F02200E}\App\thing.exe`,
		// Control Panel applet.
		`::{6C8EEC18-8D75-41B2-A177-8831D59D2D50}`: `shell:::{6C8EEC18-8D75-41B2-A177-8831D59D2D50}`,
		// Already absolute, or a real file: untouched.
		`shell:AppsFolder\Foo!App`:        `shell:AppsFolder\Foo!App`,
		`SHELL:AppsFolder\Foo!App`:        `SHELL:AppsFolder\Foo!App`,
		`C:\Windows\System32\notepad.exe`: `C:\Windows\System32\notepad.exe`,
		`\\server\share\tool.exe`:         `\\server\share\tool.exe`,
		// This PC yields a bare drive letter, which must not be read as an AUMID.
		`C:`: `C:`,
		`z:`: `z:`,
		``:   ``,
	}
	for in, want := range cases {
		if got := launchTargetFor(in); got != want {
			t.Errorf("launchTargetFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsFilesystemPath(t *testing.T) {
	cases := map[string]bool{
		`C:`: true, `C:\`: true, `C:\Windows`: true, `z:\x`: true,
		`\\server\share`: true,
		`{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\charmap.exe`: false,
		`::{6C8EEC18-8D75-41B2-A177-8831D59D2D50}`:           false,
		`shell:AppsFolder`: false,
		`Foo_bar!App`:      false,
		``:                 false,
	}
	for in, want := range cases {
		if got := isFilesystemPath(in); got != want {
			t.Errorf("isFilesystemPath(%q) = %v, want %v", in, got, want)
		}
	}
}

// Every child of every shell listing the catalog offers must resolve to a real
// icon. This is the test that would have caught both prefix bugs: on a stock
// Windows 11 desktop it failed 43 of 113 in All Apps and all 37 Control Panel
// Items. Skipped rather than failed where the shell will not enumerate, so a
// locked-down CI image does not turn red.
func TestShellListingIconsAllResolve(t *testing.T) {
	comSetup(t)
	defer purgeCaches()

	for _, root := range []string{
		"shell:AppsFolder", "shell:ControlPanelFolder", "shell:MyComputerFolder",
	} {
		entries := enumShellFolder(root, maxLimit, newBudget())
		if len(entries) == 0 {
			t.Logf("%s: no listing here, skipping", root)
			continue
		}
		generic, n := 0, 0
		for _, e := range entries {
			if root == "shell:MyComputerFolder" && !isDriveRoot(e.parsing) {
				continue
			}
			n++
			file, idx := resolveIconSource(launchTargetFor(e.parsing), "")
			if iconFor(file, idx, 16) == genericIcon() {
				generic++
				t.Errorf("%s: %q drew the generic icon (parsing %q, source %q,%d)",
					root, e.display, e.parsing, file, idx)
			}
		}
		t.Logf("%s: %d entries, %d generic", root, n, generic)
	}
}

// Live system. Skipped rather than failed where the shell refuses, so a
// locked-down CI image does not turn red -- but this is the test that catches a
// wrong BHID_EnumItems or IID_IEnumShellItems.
func TestEnumShellFolderAppsFolder(t *testing.T) {
	coInitialize()
	got := enumShellFolder("shell:AppsFolder", 50, newBudget())
	if len(got) == 0 {
		t.Skip("AppsFolder enumeration unavailable here")
	}
	for _, e := range got {
		if e.display == "" || e.parsing == "" {
			t.Errorf("incomplete entry %+v", e)
		}
	}
}

func TestIsUnnamedShellItem(t *testing.T) {
	cases := map[shellEntry]bool{
		// The nameless Control Panel registration, as the shell reports it.
		{display: `::{26EE0668-A00A-44D7-9371-BEB064C98683}\0\::{98F2AB62-0E29-4E4C-8EE7-B542E66740B1}`,
			parsing: `::{98F2AB62-0E29-4E4C-8EE7-B542E66740B1}`}: true,
		// A namespace child that is a real file or directory is called the same
		// thing in both forms, and is a perfectly good entry.
		{display: "Fonts", parsing: "Fonts"}:                                              false,
		{display: "Mouse", parsing: `::{6C8EEC18-8D75-41B2-A177-8831D59D2D50}`}:           false,
		{display: "Calculator", parsing: `Microsoft.WindowsCalculator_8wekyb3d8bbwe!App`}: false,
		{display: "Windows (C:)", parsing: `C:\`}:                                         false,
	}
	for in, want := range cases {
		if got := isUnnamedShellItem(in); got != want {
			t.Errorf("isUnnamedShellItem(%+v) = %v, want %v", in, got, want)
		}
	}
}

// Live system. The Control Panel of a stock Windows 11 holds one item with no
// name behind its CLSID, which the shell reports by handing back the parsing
// name; it used to reach the menu as an unreadable row that opened nothing.
func TestShellListingsCarryNoParsingNameLabels(t *testing.T) {
	coInitialize()
	defer purgeCaches()

	for _, root := range []string{"shell:ControlPanelFolder", "shell:AppsFolder"} {
		entries := enumShellFolder(root, maxLimit, newBudget())
		if len(entries) == 0 {
			t.Logf("%s: no listing here, skipping", root)
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.display, "::") {
				t.Errorf("%s: %q is a parsing name, not a label", root, e.display)
			}
		}
	}
}

func TestEnumShellFolderDrives(t *testing.T) {
	coInitialize()
	got := enumShellFolder("shell:MyComputerFolder", 100, newBudget())
	if len(got) == 0 {
		t.Skip("MyComputerFolder enumeration unavailable here")
	}
	drives := 0
	for _, e := range got {
		if isDriveRoot(e.parsing) {
			drives++
		}
	}
	if drives == 0 {
		t.Errorf("no drive roots among %d entries; the filter would empty the menu", len(got))
	}
}

// leafNode stores the raw path and defers BOTH halves of the icon lookup --
// source resolution and extraction -- to the first paint. That is what keeps a
// shell round-trip per entry out of WM_INITMENUPOPUP, and it is easy to undo by
// accident: storing an already-resolved source in iconFile still compiles and
// still draws, just slowly. This pins the contract and proves the deferred pair
// actually yields a real icon rather than the generic fallback.
func TestLeafNodeDefersIconLookupAndStillResolves(t *testing.T) {
	coInitialize()
	generic := genericIcon()

	cases := map[string]string{
		"executable": filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe"),
		"drive root": filepath.VolumeName(os.Getenv("SystemRoot")) + `\`,
	}
	for name, path := range cases {
		a := newTestApp()
		n := a.leafNode(name, path)

		if !n.iconLazy {
			t.Errorf("%s: iconLazy must be set", name)
		}
		if n.iconFile != path {
			t.Errorf("%s: iconFile = %q, want the raw path %q (resolution belongs at draw time)",
				name, n.iconFile, path)
		}
		if n.icon != 0 {
			t.Errorf("%s: icon extracted eagerly", name)
		}
		if n.id == 0 || a.byID[n.id] != n {
			t.Errorf("%s: not registered as a command, so it would be unclickable", name)
		}

		// What onDrawItem does on first paint.
		file, idx := resolveIconSource(n.iconFile, "")
		if h := iconFor(file, idx, 16); h == 0 || h == generic {
			t.Errorf("%s: %q resolves to %s,%d which yields no real icon", name, path, file, idx)
		}
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
