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

	id     uint32
	data   uintptr // dwItemData: index into appState.flat, offset by one
	action func()  // built-in command; when set, takes precedence over exec
}

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
			n.iconFile, n.iconIdx = resolveIconSource("", it.Icon)
			a.register(n)
			out = append(out, n)
			continue
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
		n.id = a.nextID()
		a.byID[n.id] = n
		a.register(n)
		out = append(out, n)
	}
	return out
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
		if n.separator || n.iconFile == "" {
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

	pos := uint32(0)
	a.insertPlainItem(menu, pos, &node{spacer: true, disabled: true})
	pos++

	for _, n := range nodes {
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

		if len(n.children) > 0 {
			sub := a.createMenu(n.children)
			if sub != 0 {
				mii.Mask |= miimSubMenu
				mii.SubMenu = sub
			}
		}
		insertMenuItem(menu, pos, &mii)
		pos++
	}

	a.insertPlainItem(menu, pos, &node{spacer: true, disabled: true})
	return menu
}

// insertPlainItem inserts a disabled, childless, unclickable owner-draw item
// -- used for the top/bottom spacers that give the popup the same vertical
// breathing room its items already get horizontally.
func (a *appState) insertPlainItem(menu syscall.Handle, pos uint32, n *node) {
	a.register(n)
	var mii MENUITEMINFOW
	mii.Size = uint32(unsafe.Sizeof(mii))
	mii.Mask = miimFType | miimState | miimID | miimData
	mii.Type = mftOwnerDraw
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
	head := &node{label: "⚠ Config error", disabled: true}
	detail := &node{label: msg, disabled: true}
	sep := &node{separator: true, disabled: true}
	open := &node{label: "Open config.json…", action: a.openConfig}
	open.id = a.nextID()
	reload := &node{label: "Reload", action: a.reloadInteractive}
	reload.id = a.nextID()

	nodes := []*node{head, detail, sep, open, reload}
	for _, n := range nodes {
		if n.id != 0 {
			a.byID[n.id] = n
		}
		a.register(n)
	}
	return nodes
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
		if !n.separator {
			n.id = a.nextID()
			a.byID[n.id] = n
		}
		a.register(n)
	}
	return items
}

// warningNode summarises normalisation warnings without hiding the menu.
func (a *appState) warningNode(count int) *node {
	n := &node{
		label:  fmt.Sprintf("⚠ %d config warning(s) — click to open", count),
		action: a.openConfig,
	}
	n.id = a.nextID()
	a.byID[n.id] = n
	a.register(n)
	return n
}
