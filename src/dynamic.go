//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// A dynamic submenu is one whose children are discovered when it is opened
// rather than read from the config. The whole point is that the cost is
// proportional to what the user actually opens: {"folder": "C:\\", "depth": 3}
// is a legal entry, and expanding it up front would turn every taskbar click
// into a multi-second stat storm.

type dynKind int

const (
	dynFolder   dynKind = iota // one or more directory roots, merged
	dynShell                   // a shell namespace folder, by parsing name
	dynSettings                // the curated ms-settings list
)

// dynSource describes what a dynamic submenu enumerates.
//
// Several roots rather than one because the Start Menu is genuinely two
// directories that Explorer presents as a single tree, and merging them turns
// out to be the same code as walking one folder with a one-element list.
type dynSource struct {
	kind  dynKind
	roots []string
	depth int
	limit int

	// drivesOnly filters a shell listing down to drive roots; This PC also
	// contains the Documents/Pictures/... shortcuts, which are their own
	// catalog entries.
	drivesOnly bool

	// openLabel, when set, puts an "Open X" item and a separator at the top --
	// the classic "display as menu" behaviour, and the answer to "I wanted to
	// open Downloads, not browse it".
	openLabel string
}

// Defaults and clamps for the config keys. depth is capped on purpose: the
// point of nesting is convenience, and past twenty levels a menu is worse
// than Explorer at every job it might be doing.
const (
	defaultDepth = 5
	maxDepth     = 20
	defaultLimit = 200
	maxLimit     = 2000
)

// walkBudget bounds a single population, whatever it costs per entry.
//
// nodes stops a pathological tree; deadline stops a slow one. The deadline is
// the one that matters in practice: a folder entry pointing at a disconnected
// UNC path would otherwise block the menu's modal loop until the SMB client
// gives up, with the menu frozen on screen the whole time.
type walkBudget struct {
	nodes    int
	deadline time.Time
}

const (
	budgetNodes   = 2000
	budgetTimeout = 500 * time.Millisecond
)

func newBudget() *walkBudget {
	return &walkBudget{nodes: budgetNodes, deadline: time.Now().Add(budgetTimeout)}
}

func (b *walkBudget) spent() bool {
	return b.nodes <= 0 || time.Now().After(b.deadline)
}

func (b *walkBudget) take(n int) { b.nodes -= n }

// dynSourceFor returns the description of a dynamic submenu for an item, or nil
// when the item is not one. Both routes end up here: an explicit folder, and a
// catalog special that stands for one or more well-known directories.
func dynSourceFor(it Item) *dynSource {
	if it.Folder != "" {
		return &dynSource{
			kind:      dynFolder,
			roots:     []string{it.Folder},
			depth:     clampDepth(it.Depth),
			limit:     clampLimit(it.Limit),
			openLabel: it.Label,
		}
	}
	if it.Special == "" {
		return nil
	}
	e, ok := lookupSpecial(it.Special)
	if !ok || e.kind != kindDyn {
		return nil
	}
	roots := make([]string, 0, len(e.roots))
	for _, r := range e.roots {
		if r = expandEnv(r); r != "" {
			roots = append(roots, r)
		}
	}
	d := &dynSource{
		kind:       e.dynKind,
		roots:      roots,
		depth:      clampDepth(it.Depth),
		limit:      clampLimit(it.Limit),
		drivesOnly: e.drivesOnly,
	}
	// Only a directory listing gets an "Open …" lead item; there is nothing
	// sensible to open for the settings list, and This PC already appears as a
	// Place of its own.
	if d.kind == dynFolder {
		d.openLabel = it.Label
	}
	return d
}

// dynSummary describes where a dynamic submenu's contents will come from, for
// --config-check. It lives here rather than in printItems so that adding a
// dynKind is a change to one file.
func dynSummary(d *dynSource) string {
	switch d.kind {
	case dynSettings:
		return fmt.Sprintf("%d settings pages", len(settingsPages))
	case dynShell:
		// A shell moniker is not a path; stat'ing it would report every one of
		// them as missing.
		return strings.Join(d.roots, " + ")
	default:
		roots := make([]string, 0, len(d.roots))
		for _, r := range d.roots {
			if _, err := os.Stat(r); err != nil {
				r += " [missing]"
			}
			roots = append(roots, r)
		}
		return fmt.Sprintf("depth %d, limit %d: %s", d.depth, d.limit, strings.Join(roots, " + "))
	}
}

// expand produces a dynamic submenu's children.
//
// Deliberately produces nodes and never touches an HMENU: that keeps the
// enumeration testable on its own and independent of how it happens to be
// triggered.
func (a *appState) expand(d *dynSource) []*node {
	b := newBudget()

	switch d.kind {
	case dynShell:
		return a.nonEmpty(a.expandShell(d, b))
	case dynSettings:
		return a.nonEmpty(a.expandSettings())
	}

	var out []*node
	if d.openLabel != "" && len(d.roots) > 0 {
		open := a.leafNode(fmt.Sprintf(ui.OpenFolderFormat, d.openLabel), d.roots[0])
		sep := &node{separator: true, disabled: true}
		a.register(sep)
		out = append(out, open, sep)
	}

	out = append(out, a.expandFolder(d.roots, d.depth, d.limit, b)...)
	return a.nonEmpty(out)
}

// nonEmpty replaces nothing at all with a visible "(empty)", since a popup of
// two spacers renders as a puzzling sliver.
func (a *appState) nonEmpty(out []*node) []*node {
	if len(out) == 0 {
		return []*node{a.disabledNode(ui.EmptyFolder)}
	}
	return out
}

// expandShell lists a shell namespace folder. Unlike a directory these are
// sorted by the shell already, but not always the way a menu wants, so the
// display names are re-sorted here for the same reason folders are.
func (a *appState) expandShell(d *dynSource, b *walkBudget) []*node {
	if len(d.roots) == 0 {
		return nil
	}
	entries := shellListing(d.roots[0], b)

	kept := make([]shellEntry, 0, len(entries))
	for _, e := range entries {
		if d.drivesOnly && !isDriveRoot(e.parsing) {
			continue
		}
		kept = append(kept, e)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return strings.ToLower(kept[i].display) < strings.ToLower(kept[j].display)
	})

	truncated := false
	if len(kept) > d.limit {
		kept = kept[:d.limit]
		truncated = true
	}

	out := make([]*node, 0, len(kept)+1)
	for _, e := range kept {
		// leafNode's deferred resolveIconSource handles both shapes a shell
		// child can take: a moniker goes to the image factory (a Store app's
		// package logo), and a drive root like C:\ resolves to the real drive
		// icon rather than the generic fallback a hardcoded source would give.
		out = append(out, a.leafNode(e.display, launchTargetFor(e.parsing)))
	}
	if truncated {
		out = append(out, a.moreNode(d.roots, len(kept)))
	}
	return out
}

// expandSettings turns the curated ms-settings list into a two-level tree.
func (a *appState) expandSettings() []*node {
	// Every page shares one icon, so it is resolved once rather than once per
	// page -- expandEnv is a GetEnvironmentVariableW syscall, and this runs
	// inside WM_INITMENUPOPUP.
	iconFile, iconIdx := resolveIconSource("", expandEnv(settingsIcon))

	var out []*node
	group := ""
	var current *node
	for _, s := range settingsPages {
		if s.group != group {
			group = s.group
			current = &node{label: settingsGroupLabel(s.group)}
			current.iconFile, current.iconIdx = iconFile, iconIdx
			a.register(current)
			out = append(out, current)
		}
		n := a.registerCommand(&node{label: settingsPageLabel(s), exec: s.uri, show: showNormal})
		n.iconFile, n.iconIdx = iconFile, iconIdx
		current.children = append(current.children, n)
	}
	return out
}

// expandFolder lists one level of one or more merged roots.
func (a *appState) expandFolder(roots []string, depth, limit int, b *walkBudget) []*node {
	if depth <= 0 || b.spent() {
		return nil
	}

	lists := make([][]dirEntry, 0, len(roots))
	for _, root := range roots {
		// A missing root is normal rather than exceptional: the per-user Start
		// Menu tree does not exist on a fresh profile, and %PUBLIC%\Desktop may
		// be absent. Only every root failing produces an empty menu.
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		lists = append(lists, collectEntries(root, ents))
	}

	merged := mergeLevels(lists)
	sortDirEntries(merged)

	truncated := false
	if len(merged) > limit {
		merged = merged[:limit]
		truncated = true
	}
	b.take(len(merged))

	out := make([]*node, 0, len(merged)+1)
	for _, e := range merged {
		if e.dir && depth > 1 {
			n := &node{label: e.label}
			n.dyn = &dynSource{kind: dynFolder, roots: e.roots, depth: depth - 1, limit: limit}
			n.iconFile, n.iconLazy = e.path, true
			a.register(n)
			out = append(out, n)
			continue
		}
		out = append(out, a.leafNode(e.label, e.path))
	}

	if truncated || b.spent() {
		out = append(out, a.moreNode(roots, len(merged)))
	}
	return out
}

// leafNode is one enumerated entry. iconFile holds the raw path, not a resolved
// source: both the resolution and the extraction wait for the first paint, so a
// level of two hundred entries costs neither on the way in. See onDrawItem.
func (a *appState) leafNode(label, path string) *node {
	n := a.registerCommand(&node{label: label, exec: path, show: showNormal})
	n.iconFile, n.iconLazy = path, true
	return n
}

func (a *appState) disabledNode(label string) *node {
	n := &node{label: label, disabled: true}
	a.register(n)
	return n
}

// moreNode is the overflow entry. It is enabled and opens the containing folder,
// so hitting the limit is a door rather than a dead end.
func (a *appState) moreNode(roots []string, shown int) *node {
	if len(roots) > 0 {
		return a.leafNode(fmt.Sprintf(ui.MoreFormat, shown), roots[0])
	}
	return a.disabledNode(ui.More)
}

// hiddenOrSystem keeps the entries Explorer hides out of the menu. Without it
// the Start Menu tree shows desktop.ini siblings and a user's profile folder
// shows AppData, NTUSER.DAT and a dozen other things nobody put there to click.
func hiddenOrSystem(info os.FileInfo) bool {
	d, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	const fileAttributeHidden, fileAttributeSystem = 0x2, 0x4
	return d.FileAttributes&(fileAttributeHidden|fileAttributeSystem) != 0
}

// ---------------------------------------------------------------------------
// Shell listing cache
// ---------------------------------------------------------------------------

// Enumerating the AppsFolder takes 100-300ms, and WM_INITMENUPOPUP runs inside
// the menu's modal loop, so the flyout visibly stalls for that long. The set of
// installed applications changes on install and uninstall, not between two
// clicks, so the listing is cached for the process lifetime with a short TTL.
//
// Only the strings are cached, never the icons: those are DPI-dependent and
// belong to iconFor's own cache.
type shellListingEntry struct {
	entries []shellEntry
	at      time.Time
}

var shellListings = map[string]shellListingEntry{}

const shellListingTTL = 5 * time.Minute

// shellListing always enumerates up to maxLimit rather than the entry's own
// limit: the cache is shared by every entry naming the same folder, so it must
// hold the fullest listing any of them could ask for. Trimming happens per entry
// in expandShell.
func shellListing(parsingName string, b *walkBudget) []shellEntry {
	if c, ok := shellListings[parsingName]; ok && time.Since(c.at) < shellListingTTL {
		return c.entries
	}
	entries := enumShellFolder(parsingName, maxLimit, b)
	if len(entries) == 0 {
		return nil // do not cache a failure; the next open should retry
	}
	shellListings[parsingName] = shellListingEntry{entries: entries, at: time.Now()}
	return entries
}

// purgeShellListings drops the cache on config reload, so "Reload config" is
// also the answer to "I just installed something and it is not in the list".
func purgeShellListings() { clear(shellListings) }

// ---------------------------------------------------------------------------
// Directory listing -- pure logic, no Win32
// ---------------------------------------------------------------------------

type dirEntry struct {
	label string
	path  string
	dir   bool
	// roots carries every directory this entry was merged from, so the level
	// below can merge in turn.
	roots []string
}

// collectEntries turns one ReadDir result into level entries, dropping what a
// menu should never show.
func collectEntries(root string, ents []os.DirEntry) []dirEntry {
	// desktop.ini is the only thing that can rename a file, so the level can
	// decide once whether any of its files need the lookup at all -- and almost
	// no folder on the machine has one. See localizedName.
	renames := hasDesktopIni(ents)

	out := make([]dirEntry, 0, len(ents))
	for _, e := range ents {
		name := e.Name()
		if skipEntry(name) {
			continue
		}
		// A symlink or reparse point is how a directory walk becomes infinite;
		// junctions under a user profile point back up the tree routinely.
		if e.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if hiddenOrSystem(info) {
			continue
		}
		full := filepath.Join(root, name)
		d := dirEntry{label: displayName(name, e.IsDir()), path: full, dir: e.IsDir()}
		// A directory is asked whatever this level says, because the desktop.ini
		// that renames a folder is the one inside it, which the level above
		// cannot see.
		if renames || d.dir {
			if l := localizedName(full); l != "" {
				d.label = l
			}
		}
		if d.dir {
			d.roots = []string{full}
		}
		out = append(out, d)
	}
	return out
}

// desktopIni is both the file a listing hides and the file that decides whether
// anything in that listing is renamed.
const desktopIni = "desktop.ini"

func hasDesktopIni(ents []os.DirEntry) bool {
	for _, e := range ents {
		if strings.EqualFold(e.Name(), desktopIni) {
			return true
		}
	}
	return false
}

func skipEntry(name string) bool {
	return strings.EqualFold(name, desktopIni)
}

// displayName is what the entry is called in the menu: the file name with a
// shortcut extension removed, because "Firefox.lnk" is not what anyone calls it.
// Every other extension is left alone -- hiding ".txt" would make two different
// files look identical.
func displayName(name string, isDir bool) string {
	if isDir {
		return name
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".lnk" || ext == ".url" {
		return name[:len(name)-len(ext)]
	}
	return name
}

// mergeLevels folds the listings of several roots into one.
//
// Directories with the same display name become a single entry carrying every
// matching path, which is what makes the two Start Menu trees read as the one
// menu Explorer shows rather than two half-menus; recursion then re-merges each
// level below. Files with the same name de-duplicate, with the first list
// winning -- callers pass the per-user root first, since that is the copy the
// user can actually edit.
//
// Comparison is case-insensitive because Windows paths are.
func mergeLevels(lists [][]dirEntry) []dirEntry {
	var out []dirEntry
	index := map[string]int{}

	for _, list := range lists {
		for _, e := range list {
			key := strings.ToLower(e.label) + "\x00"
			if e.dir {
				key += "d"
			} else {
				key += "f"
			}
			if at, ok := index[key]; ok {
				if e.dir {
					out[at].roots = append(out[at].roots, e.roots...)
				}
				// A duplicate file keeps the first one seen.
				continue
			}
			index[key] = len(out)
			out = append(out, e)
		}
	}
	return dropShadowedFiles(out)
}

// dropShadowedFiles removes a file whose display name matches a directory's at
// the same level.
//
// This is not hypothetical tidying: the per-user Start Menu ships both an
// "Administrative Tools" folder and an "Administrative Tools.lnk" pointing at
// it, so the merged level would otherwise show the same words twice, one of
// them with a flyout and one without. Keeping the directory keeps the entry
// that can actually be browsed.
func dropShadowedFiles(in []dirEntry) []dirEntry {
	dirs := make(map[string]bool, len(in))
	for _, e := range in {
		if e.dir {
			dirs[strings.ToLower(e.label)] = true
		}
	}
	out := in[:0]
	for _, e := range in {
		if !e.dir && dirs[strings.ToLower(e.label)] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// sortDirEntries orders a level: directories first, then files, each group
// case-insensitively by the *display* label with the raw path as a stable
// tiebreak.
//
// It is what Explorer does and what the classic cascading Start menu did, so it
// is what the muscle memory expects. Sorting on the label rather than the file
// name is the part that matters in a Start Menu: it files "Firefox.lnk" under F
// where someone looking for Firefox will look.
func sortDirEntries(e []dirEntry) {
	sort.SliceStable(e, func(i, j int) bool {
		if e[i].dir != e[j].dir {
			return e[i].dir
		}
		li, lj := strings.ToLower(e[i].label), strings.ToLower(e[j].label)
		if li != lj {
			return li < lj
		}
		return strings.ToLower(e[i].path) < strings.ToLower(e[j].path)
	})
}

// clampDepth and clampLimit keep a hand-edited config inside the range the
// budget was sized for.
func clampDepth(d int) int {
	switch {
	case d <= 0:
		return defaultDepth
	case d > maxDepth:
		return maxDepth
	default:
		return d
	}
}

func clampLimit(l int) int {
	switch {
	case l <= 0:
		return defaultLimit
	case l > maxLimit:
		return maxLimit
	default:
		return l
	}
}
