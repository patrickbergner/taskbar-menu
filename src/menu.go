//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// node is a menu entry resolved for the current DPI and theme. It is the
// runtime counterpart of Item: paths already expanded, icon already extracted,
// command id already assigned.
type node struct {
	label     string
	separator bool
	spacer    bool
	disabled  bool
	children  []*node

	exec     string
	appID    string // AppUserModelID of a Store app; launched via shell:AppsFolder
	args     []string
	cwd      string
	elevated bool
	show     int32

	iconFile string
	iconIdx  int32
	icon     syscall.Handle
	// iconLazy defers extraction to the first paint. A dynamic submenu can hold
	// hundreds of entries of which twenty are ever visible, and extracting the
	// rest would happen inside WM_INITMENUPOPUP with the menu frozen.
	iconLazy bool

	// dyn marks an entry whose children are discovered when it is opened rather
	// than read from the config; populated stops that happening twice within one
	// menu build. See dynamic.go.
	dyn       *dynSource
	populated bool

	id     uint32
	data   uintptr // dwItemData: index into appState.flat, offset by one
	action func()  // built-in command; when set, takes precedence over exec
}

// hasSubmenu reports whether the entry opens a flyout.
//
// Not the same question as "does it have children": a dynamic entry has none
// until WM_INITMENUPOPUP fills it, yet it must measure, draw and arrow itself as
// a submenu from the very first frame -- Windows paints the arrow regardless, so
// an entry that measured itself as a leaf runs its label underneath it.
func (n *node) hasSubmenu() bool { return len(n.children) > 0 || n.dyn != nil }

// buildNodes converts config items into runtime nodes, assigning command ids
// and registering each node in the flat slice that WM_DRAWITEM indexes into.
func (a *appState) buildNodes(items []Item) []*node {
	out := make([]*node, 0, len(items))
	for i := range items {
		it := items[i]
		n := &node{label: it.Label}

		if it.Type == "separator" {
			n.separator = true
			n.disabled = true
			a.register(n)
			out = append(out, n)
			continue
		}

		if len(it.Items) > 0 {
			n.children = a.buildNodes(it.Items)
			if it.OpenAll {
				n.children = a.withOpenAll(n.children)
			}
			n.iconFile, n.iconIdx = resolveIconSource("", it.Icon)
			a.register(n)
			out = append(out, n)
			continue
		}

		// A dynamic submenu: no children yet, just a description of how to find
		// them when the flyout is opened.
		if d := dynSourceFor(it); d != nil {
			n.dyn = d
			iconFrom := ""
			if len(d.roots) > 0 {
				iconFrom = d.roots[0]
			}
			n.iconFile, n.iconIdx = resolveIconSource(iconFrom, it.Icon)
			a.register(n)
			out = append(out, n)
			continue
		}

		// A special that survived normalisation names a built-in rather than a
		// target -- currently only the power actions. invoke() prefers action
		// over exec, so nothing else here has to change.
		if it.Special != "" {
			n.action = a.powerAction(it.Special, it.confirms())
		}

		n.exec = it.Exec
		n.appID = it.AppID
		n.args = it.Args
		n.cwd = it.Cwd
		n.elevated = it.Elevated
		n.show = it.showCmd()
		// A Store app has no exec to draw an icon from, but its AppsFolder
		// moniker resolves to the package logo through the shell image factory.
		iconExec := it.Exec
		if it.AppID != "" {
			iconExec = appsFolderPrefix + it.AppID
		}
		n.iconFile, n.iconIdx = resolveIconSource(iconExec, it.Icon)
		a.registerCommand(n)
		out = append(out, n)
	}
	return out
}

// withOpenAll prepends a one-click "Open all" entry and a separator below it
// to a submenu's children -- the power-user answer to a submenu that is
// really just a set of things opened together every time, one click at a
// time otherwise.
func (a *appState) withOpenAll(children []*node) []*node {
	targets := collectLaunchTargets(children)
	if len(targets) == 0 {
		return children
	}
	open := a.registerCommand(&node{
		label:  "Open all",
		action: func() { a.launchAll(targets) },
	})
	sep := &node{separator: true, disabled: true}
	a.register(sep)
	return append([]*node{open, sep}, children...)
}

// collectLaunchTargets gathers every launchable leaf under a submenu,
// recursing into nested submenus so "Open all" reaches entries at any depth.
// A separator or spacer contributes nothing, a built-in action (a power
// command, an already-collected "Open all" itself) is deliberately excluded --
// batch-firing "Sign out" or "Shut down" alongside a browser is not what
// "open all" should mean -- and a dynamic submenu is skipped because its
// children only exist once opened; there is nothing here yet to collect.
func collectLaunchTargets(nodes []*node) []*node {
	var out []*node
	for _, n := range nodes {
		switch {
		case n.separator || n.spacer || n.dyn != nil:
			continue
		case len(n.children) > 0:
			out = append(out, collectLaunchTargets(n.children)...)
		case n.action == nil && (n.exec != "" || n.appID != ""):
			out = append(out, n)
		}
	}
	return out
}

// launchAll runs every target in turn. A failure is reported the same way a
// single entry's is, rather than aborting the rest -- one bad path in a batch
// of ten should not silently swallow the other nine.
func (a *appState) launchAll(targets []*node) {
	for _, n := range targets {
		if err := launch(n); err != nil {
			a.notify("TaskbarMenu", "Could not start "+n.label+"\n"+err.Error(), niifError)
		}
	}
}

// registerCommand makes a node clickable: it needs a command id for
// TrackPopupMenuEx to return, an entry in byID for invoke to find it again, and
// a place in flat for WM_DRAWITEM. Dropping any one of the three yields an item
// that draws but does nothing, or does not draw at all -- with no compile error
// either way, which is why all three live in one helper.
func (a *appState) registerCommand(n *node) *node {
	n.id = a.nextID()
	a.byID[n.id] = n
	a.register(n)
	return n
}

// register records the node so DRAWITEMSTRUCT.ItemData can point back at it.
// dwItemData holds index+1 because 0 has to mean "not one of ours" -- menus we
// did not build (and the system's own items) arrive with dwItemData zero.
func (a *appState) register(n *node) {
	a.flat = append(a.flat, n)
	n.data = uintptr(len(a.flat))
}

func (a *appState) nodeFromData(data uintptr) *node {
	if data == 0 || data > uintptr(len(a.flat)) {
		return nil
	}
	return a.flat[data-1]
}

func (a *appState) nextID() uint32 {
	a.lastID++
	return a.lastID
}

// resolveIcons extracts every icon at the current DPI's pixel size. Cached per
// (file, index, size), so switching monitors re-extracts once and then hits the
// cache forever after.
func (a *appState) resolveIcons(nodes []*node) {
	for _, n := range nodes {
		if len(n.children) > 0 {
			a.resolveIcons(n.children)
		}
		if n.separator || n.iconFile == "" || n.iconLazy {
			continue
		}
		n.icon = iconFor(n.iconFile, n.iconIdx, a.metrics.iconSize)
	}
}

// createMenu turns a node slice into an HMENU. Every item is owner-draw,
// including separators: an item that is both MFT_OWNERDRAW and MFT_SEPARATOR
// behaves inconsistently across Windows versions, so separators are ordinary
// owner-draw items that happen to paint a line and refuse selection.
func (a *appState) createMenu(nodes []*node) syscall.Handle {
	menu := createPopupMenu()
	if menu == 0 {
		return 0
	}
	a.fillMenu(menu, nodes)
	return menu
}

// fillMenu replaces a popup's contents, spacers and all.
//
// Split out of createMenu so WM_INITMENUPOPUP can swap a dynamic submenu's
// placeholder for real entries and still get the same top-and-bottom breathing
// room every other popup has. Existing items are deleted first, which makes
// re-filling an already-populated menu safe.
func (a *appState) fillMenu(menu syscall.Handle, nodes []*node) {
	for getMenuItemCount(menu) > 0 {
		if !deleteMenu(menu, 0) {
			break // refuse to spin if the menu is somehow not ours
		}
	}

	breaks := a.columnBreaks(nodes)

	pos := uint32(0)
	a.insertSpacer(menu, pos, 0)
	pos++

	for i, n := range nodes {
		if breaks != nil && breaks[i] {
			// Close the outgoing column and open the incoming one, so every
			// column gets the same frame a single-column popup has. The break
			// rides on the opening spacer rather than on the item, which would
			// otherwise start its column flush against the top edge.
			a.insertSpacer(menu, pos, 0)
			pos++
			a.insertSpacer(menu, pos, mftMenuBreak)
			pos++
		}

		var mii MENUITEMINFOW
		mii.Size = uint32(unsafe.Sizeof(mii))
		mii.Mask = miimFType | miimState | miimID | miimData
		mii.Type = mftOwnerDraw
		mii.ID = n.id
		mii.ItemData = n.data

		if n.separator || n.disabled {
			mii.State = mfsDisabled
		} else {
			mii.State = mfsEnabled
		}

		switch {
		case n.dyn != nil && !n.populated:
			// A submenu holding a single disabled "…" rather than an empty one:
			// an empty HMENU may not draw an arrow at all, and this way a
			// submenu that somehow never gets populated is visibly broken
			// instead of silently absent.
			sub := a.createMenu([]*node{a.placeholderNode()})
			if sub != 0 {
				a.dynMenus[sub] = n
				mii.Mask |= miimSubMenu
				mii.SubMenu = sub
			}
		case n.hasSubmenu():
			sub := a.createMenu(n.children)
			if sub != 0 {
				mii.Mask |= miimSubMenu
				mii.SubMenu = sub
			}
		}
		insertMenuItem(menu, pos, &mii)
		pos++
	}

	a.insertSpacer(menu, pos, 0)
}

// columnBreaks decides which entries begin a new column.
//
// A popup taller than the screen otherwise falls back to Windows' own scrolling,
// which hides most of the menu behind two arrows and turns reaching the far end
// into a held mouse button. Laying the overflow out sideways keeps all of it on
// screen and one click away. Returns nil to leave a popup alone -- because it
// already fits, which is the common case and costs nothing past the measuring
// pass, or because it is too big for even a screen full of columns.
//
// Windows picks no break points of its own: MFT_MENUBREAK only says "this item
// starts a column", so the heights have to be predicted here, before the menu
// window -- and with it the first WM_MEASUREITEM -- exists at all. measure() is
// shared with onMeasureItem precisely so the two cannot disagree.
func (a *appState) columnBreaks(nodes []*node) []bool {
	if a.work.Bottom <= a.work.Top {
		return nil // no work area known; leave the layout to Windows
	}

	hdc := getDC(0)
	defer releaseDC(0, hdc)

	heights := make([]int32, len(nodes))
	var widest int32
	for i, n := range nodes {
		w, h := a.measure(hdc, n)
		heights[i] = h
		if w > widest {
			widest = w
		}
	}
	// A column comes out wider than its widest entry: Windows reserves the
	// check-mark gutter on every item on top of the width WM_MEASUREITEM asked
	// for. Measured at 24px a column on a 144 DPI screen, which is exactly what
	// SM_CXMENUCHECK reports there. Left out, the width each column is costed at
	// below comes in around 15% short, and a wide enough menu is judged to fit
	// the screen when it does not.
	colW := widest
	if colW > 0 {
		colW += getSystemMetricsForDpi(smCXMenuCheck, a.metrics.dpi)
	}
	// Every column carries the same top and bottom spacer as a single-column
	// popup, so that much of the height is spent before any item goes in.
	breaks := splitColumns(heights, colW,
		(a.work.Bottom-a.work.Top)-2*a.metrics.frameH, a.work.Right-a.work.Left)
	if breaks != nil {
		cols := 1
		for _, b := range breaks {
			if b {
				cols++
			}
		}
		logf("column split: %d entries, %d columns, colW=%d work=%dx%d",
			len(nodes), cols, colW, a.work.Right-a.work.Left, a.work.Bottom-a.work.Top)
	}
	return breaks
}

// splitColumns marks the entries that begin a new column, given each entry's
// height, the width one column will take, and the room available.
//
// Kept free of both Windows and appState so the arithmetic -- the part that is
// wrong by an off-by-one rather than by an API misuse -- can be tested directly.
func splitColumns(heights []int32, colW, availH, availW int32) []bool {
	// One entry cannot be split off from itself however tall it is, and the
	// caller reads a non-nil result as "there are columns".
	if availH <= 0 || len(heights) < 2 {
		return nil
	}
	var total int32
	for _, h := range heights {
		total += h
	}
	if total <= availH {
		return nil
	}

	// How many columns will stand side by side on the screen. Costing each at
	// the widest entry is pessimistic, and pessimistic is the right way to be
	// wrong: splitting is all-or-nothing, because Windows scrolls a single
	// overlong column but not a multi-column one -- it sizes the popup to
	// whatever the columns ask for and lets it run off the screen, where the
	// overflow can be reached by nothing at all.
	most := len(heights)
	if colW > 0 {
		most = int(availW / colW)
	}

	// An even split into just-enough columns can still leave one of them over
	// budget: the entries are lumpy, and the rounding puts one more of them in
	// some column than the average has room for. Another column is always the
	// cure -- it can only make every column shorter -- so widen the grid until
	// one fits, or until the screen runs out of room and Windows' own scrolling
	// stays the better answer.
	for cols := int((total + availH - 1) / availH); cols <= most; cols++ {
		breaks := balanceColumns(heights, total, cols)
		if tallestColumn(heights, breaks) <= availH {
			return breaks
		}
	}
	return nil
}

// balanceColumns divides heights into cols columns of as near equal height as
// the entries allow, by comparing each entry's midpoint against the boundaries
// of an even division and dropping it in whichever column it mostly falls into.
// Equal columns read as deliberate, where filling each to the brim and trailing
// a three-entry stub reads as a bug.
func balanceColumns(heights []int32, total int32, cols int) []bool {
	breaks := make([]bool, len(heights))
	col, run := 1, int32(0)
	for i, h := range heights {
		if i > 0 && col < cols && run+h/2 > int32(col)*total/int32(cols) {
			breaks[i] = true
			col++
		}
		run += h
	}
	return breaks
}

// tallestColumn is the height of the longest column a break slice produces.
func tallestColumn(heights []int32, breaks []bool) int32 {
	var tallest, run int32
	for i, h := range heights {
		if breaks[i] {
			if run > tallest {
				tallest = run
			}
			run = 0
		}
		run += h
	}
	if run > tallest {
		return run
	}
	return tallest
}

// placeholderNode is what a dynamic submenu holds until it is opened.
func (a *appState) placeholderNode() *node { return a.disabledNode("…") }

// insertSpacer inserts a disabled, childless, unclickable owner-draw item --
// the top and bottom padding that gives a popup the same vertical breathing
// room its items already get horizontally. fType carries mftMenuBreak when this
// spacer is the one opening a new column; see columnBreaks.
func (a *appState) insertSpacer(menu syscall.Handle, pos uint32, fType uint32) {
	n := &node{spacer: true, disabled: true}
	a.register(n)
	var mii MENUITEMINFOW
	mii.Size = uint32(unsafe.Sizeof(mii))
	mii.Mask = miimFType | miimState | miimID | miimData
	mii.Type = mftOwnerDraw | fType
	mii.State = mfsDisabled
	mii.ItemData = n.data
	insertMenuItem(menu, pos, &mii)
}

// applyMenuBackground paints the frame the system owns. Setting hbrBack is the
// documented way to darken it, at the cost of disabling theming for the menu --
// which trades Windows 11's rounded corners for a flat rectangle. The border
// this leaves behind is repainted separately; see border.go.
func (a *appState) applyMenuBackground(menu syscall.Handle) {
	if menu == 0 {
		return
	}
	var mi MENUINFO
	mi.Size = uint32(unsafe.Sizeof(mi))
	mi.Mask = mimBackground | mimApplyToSubMenus
	mi.Back = a.bgBrush
	setMenuInfo(menu, &mi)
}

// errorNodes render a config failure as a usable menu instead of an empty one.
// Combined with hot reload this makes fixing the file self-correcting: save,
// click again, see the result.
func (a *appState) errorNodes(msg string) []*node {
	head := a.disabledNode("⚠ Config error")
	detail := a.disabledNode(msg)
	sep := &node{separator: true, disabled: true}
	a.register(sep)
	open := a.registerCommand(&node{label: "Open config.json…", action: a.openConfig})
	reload := a.registerCommand(&node{label: "Reload", action: a.reloadInteractive})
	return []*node{head, detail, sep, open, reload}
}

// trayNodes is the right-click menu on the notification icon.
func (a *appState) trayNodes() []*node {
	items := []*node{
		{label: "Show menu", action: func() { a.showMenu(getCursorPos()) }},
		{separator: true, disabled: true},
		{label: "Reload config", action: a.reloadInteractive},
		{label: "Edit config…", action: a.openConfig},
		{separator: true, disabled: true},
		{label: "Exit", action: func() { postQuitMessage(0) }},
	}
	for _, n := range items {
		if n.separator {
			a.register(n)
			continue
		}
		a.registerCommand(n)
	}
	return items
}

// warningNode summarises normalisation warnings without hiding the menu.
func (a *appState) warningNode(count int) *node {
	return a.registerCommand(&node{
		label:  fmt.Sprintf("⚠ %d config warning(s) — click to open", count),
		action: a.openConfig,
	})
}
