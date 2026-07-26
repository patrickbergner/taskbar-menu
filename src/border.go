//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// Setting a background brush on the menu (see menu.go) disables theming for
// it, which drops the popup back to classic, non-visual-styled chrome: the
// 1px nonclient border reverts to its classic light-gray colour, and the
// flyout arrow Windows paints over every item with a submenu (see draw.go)
// reverts to hardcoded classic system colours -- both ignore the app's dark
// palette, and neither has a documented way to recolour it directly. So this
// hooks the window's creation instead:
//
//   - installBorderHook arms a thread-local WH_CBT hook for the lifetime of
//     one TrackPopupMenuEx call (see track() in main.go).
//   - The hook fires HCBT_CREATEWND for every window the thread creates,
//     including the top-level menu and any open submenu. Each one that turns
//     out to be a popup-menu window ("#32768") gets its WNDPROC swapped for
//     borderWndProc.
//   - borderWndProc lets the original WNDPROC run first, then repaints the
//     border after WM_NCPAINT, repaints every submenu arrow after WM_PAINT --
//     Windows draws its own arrow after WM_DRAWITEM returns, so it can only be
//     overdrawn once painting is otherwise finished -- and drops its
//     bookkeeping on WM_NCDESTROY.
//
// Everything here runs on the single OS thread the whole app is pinned to
// (see main()), so the package state below needs no locking.
var (
	cbtCallback        = syscall.NewCallback(cbtHookProc)
	borderSubclassProc = syscall.NewCallback(borderWndProc)

	borderHook      syscall.Handle
	borderColor     uint32
	borderOrigProcs = map[syscall.Handle]uintptr{}
)

func installBorderHook(colour uint32) {
	borderColor = colour
	borderHook = setWindowsHookEx(whCbt, cbtCallback, getCurrentThreadID())
}

func uninstallBorderHook() {
	unhookWindowsHookEx(borderHook)
	borderHook = 0
	for h := range borderOrigProcs {
		delete(borderOrigProcs, h)
	}
}

func cbtHookProc(nCode, wp, lp uintptr) uintptr {
	if nCode == hcbtCreateWnd {
		hwnd := syscall.Handle(wp)
		if getClassName(hwnd) == "#32768" {
			orig := setWindowLongPtr(hwnd, gwlpWndProc, borderSubclassProc)
			if orig != 0 {
				borderOrigProcs[hwnd] = orig
			}
		}
	}
	return callNextHookEx(borderHook, nCode, wp, lp)
}

func borderWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	h := syscall.Handle(hwnd)
	orig := borderOrigProcs[h]
	r := callWindowProc(orig, h, uint32(msg), wp, lp)
	switch uint32(msg) {
	case wmNcPaint:
		paintBorder(h, borderColor)
	case wmPaint:
		paintSubmenuArrows(h)
	case wmNcDestroy:
		delete(borderOrigProcs, h)
	}
	return r
}

// paintSubmenuArrows re-paints the flyout arrow for every item in hwnd's menu
// that has a submenu, in the app's own palette instead of the classic system
// colours Windows just used. hwnd is a popup-menu window ("#32768"); its HMENU
// is not otherwise obtainable from the window handle, so this uses the
// MN_GETHMENU message every other menu-introspection trick relies on.
func paintSubmenuArrows(hwnd syscall.Handle) {
	menu := syscall.Handle(sendMessage(hwnd, mnGetHMenu, 0, 0))
	if menu == 0 {
		return
	}
	count := getMenuItemCount(menu)
	if count <= 0 {
		return
	}

	var winRect RECT
	if !getWindowRect(hwnd, &winRect) {
		return
	}
	hdc := getWindowDC(hwnd)
	if hdc == 0 {
		return
	}
	defer releaseDC(hwnd, hdc)

	a := &app
	for i := uint32(0); i < uint32(count); i++ {
		var mii MENUITEMINFOW
		mii.Size = uint32(unsafe.Sizeof(mii))
		mii.Mask = miimData | miimState
		if !getMenuItemInfoByPos(menu, i, &mii) {
			continue
		}
		n := a.nodeFromData(mii.ItemData)
		if n == nil || !n.hasSubmenu() {
			continue
		}

		rc, ok := getMenuItemRect(hwnd, menu, i)
		if !ok {
			continue
		}
		rc = RECT{rc.Left - winRect.Left, rc.Top - winRect.Top, rc.Right - winRect.Left, rc.Bottom - winRect.Top}

		bg := a.palette.bg
		if mii.State&mfsHilite != 0 {
			bg = a.palette.hot
		}
		colour := a.palette.text
		if n.disabled {
			colour = a.palette.disabled
		}
		a.paintSubmenuArrow(hdc, rc, bg, colour)
	}
}

// paintBorder overdraws the window's outer 1px edge in colour, on top of
// whatever the default nonclient painting just drew.
func paintBorder(hwnd syscall.Handle, colour uint32) {
	hdc := getWindowDC(hwnd)
	if hdc == 0 {
		return
	}
	defer releaseDC(hwnd, hdc)

	var rc RECT
	if !getWindowRect(hwnd, &rc) {
		return
	}

	pen := createPen(psSolid, 1, colour)
	defer deleteObject(pen)
	oldPen := selectObject(hdc, pen)
	oldBrush := selectObject(hdc, getStockObject(nullBrush))
	rectangle(hdc, 0, 0, rc.Right-rc.Left, rc.Bottom-rc.Top)
	selectObject(hdc, oldPen)
	selectObject(hdc, oldBrush)
}
