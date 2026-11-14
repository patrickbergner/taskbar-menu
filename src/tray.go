//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

// The tray icon keeps the resident server discoverable and gives it somewhere
// to report errors. It is optional: with "trayIcon": false the process still
// stays resident, it just becomes invisible.

func (a *appState) notifyData(flags uint32) NOTIFYICONDATAW {
	var nid NOTIFYICONDATAW
	nid.Size = uint32(unsafe.Sizeof(nid))
	nid.Wnd = a.hwnd
	nid.ID = 1
	nid.Flags = flags
	nid.CallbackMessage = wmTrayIcon
	return nid
}

func (a *appState) wantsTrayIcon() bool {
	return a.cfg != nil && a.cfg.TrayIcon != nil && *a.cfg.TrayIcon
}

// syncTrayIcon makes the notification icon match the config after a reload:
// added when "trayIcon" was switched on, removed when it was switched off, and
// otherwise just given the current tooltip. Without the first two, editing the
// key needed a restart of the resident server to take effect.
func (a *appState) syncTrayIcon() {
	if a.hwnd == 0 {
		return // no window to own an icon yet (or a test's bare appState)
	}
	switch want := a.wantsTrayIcon(); {
	case want && !a.trayAdded:
		a.addTrayIcon()
	case !want && a.trayAdded:
		a.removeTrayIcon()
	default:
		a.refreshTrayTip()
	}
}

func (a *appState) addTrayIcon() {
	if a.trayAdded || !a.wantsTrayIcon() {
		return
	}
	nid := a.notifyData(nifMessage | nifIcon | nifTip)
	nid.Icon = a.trayIcon()
	copyUTF16(nid.Tip[:], a.tipText())

	if shellNotifyIcon(nimAdd, &nid) {
		a.trayAdded = true
	}
}

func (a *appState) removeTrayIcon() {
	if !a.trayAdded {
		return
	}
	nid := a.notifyData(0)
	shellNotifyIcon(nimDelete, &nid)
	a.trayAdded = false
}

func (a *appState) refreshTrayTip() {
	if !a.trayAdded {
		return
	}
	nid := a.notifyData(nifTip)
	copyUTF16(nid.Tip[:], a.tipText())
	shellNotifyIcon(nimModify, &nid)
}

// tipText names the config file so several resident instances, one per config,
// stay tellable apart in the notification area.
func (a *appState) tipText() string {
	name := filepath.Base(a.cfgPath)
	if a.cfgErr != nil {
		return fmt.Sprintf(ui.TooltipErrorFormat, windowTitle, name)
	}
	if n := len(a.cfg.Warnings); n > 0 {
		return fmt.Sprintf(ui.TooltipWarningsFormat, windowTitle, name, n)
	}
	return fmt.Sprintf(ui.TooltipFormat, windowTitle, name)
}

// trayIcon uses the exe's own embedded icon so the notification area matches
// whatever icon was stamped in by the build.
//
// The handle is kept and reused: the icon is re-added after every Explorer
// restart and every time "trayIcon" is switched back on, and extracting a fresh
// one each time leaked the previous handle. It is only replaced when the
// primary monitor's DPI asks for a different size.
func (a *appState) trayIcon() syscall.Handle {
	size := getSystemMetricsForDpi(smCXSmIcon, a.primaryDPI())
	if size <= 0 {
		size = 16
	}
	if a.trayHIcon != 0 && a.trayHIconSize == size {
		return a.trayHIcon
	}
	a.freeTrayIcon()
	if h := extractIcon(a.exePath, 0, size); h != 0 {
		a.trayHIcon, a.trayHIconSize = h, size
		return h
	}
	return genericIcon()
}

// freeTrayIcon destroys the kept tray icon. The shared generic icon is never
// stored there, so nothing system-owned is destroyed.
func (a *appState) freeTrayIcon() {
	destroyIcon(a.trayHIcon)
	a.trayHIcon, a.trayHIconSize = 0, 0
}

// notify surfaces a message as a balloon, falling back to a message box when
// the tray icon is switched off. Launch failures must never be silent, but they
// must also never block the UI thread behind a modal dialog if avoidable.
func (a *appState) notify(title, msg string, icon uint32) {
	if a.trayAdded {
		nid := a.notifyData(nifInfo)
		copyUTF16(nid.InfoTitle[:], title)
		copyUTF16(nid.Info[:], msg)
		nid.InfoFlags = icon
		if shellNotifyIcon(nimModify, &nid) {
			return
		}
	}
	flags := uint32(mbOK | mbIconInformation)
	if icon == niifError {
		flags = mbOK | mbIconError
	}
	messageBox(title, msg, flags)
}

// onTrayMessage handles clicks on the notification icon. Left click shows the
// menu, right click shows the maintenance menu.
func (a *appState) onTrayMessage(lp uintptr) {
	switch uint32(lp) {
	case wmLButtonUp:
		a.showMenu(getCursorPos())
	case wmRButtonUp:
		a.showTrayMenu(getCursorPos())
	}
}
