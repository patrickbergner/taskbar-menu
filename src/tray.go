//go:build windows

package main

import (
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

func (a *appState) addTrayIcon() {
	if a.cfg.TrayIcon == nil || !*a.cfg.TrayIcon {
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
		return "TaskbarMenu — " + name + " — config error"
	}
	if n := len(a.cfg.Warnings); n > 0 {
		return "TaskbarMenu — " + name + " — " + itoa(n) + " config warning(s)"
	}
	return "TaskbarMenu — " + name
}

// trayIcon uses the exe's own embedded icon so the notification area matches
// whatever icon was stamped in by the build.
func (a *appState) trayIcon() syscall.Handle {
	size := getSystemMetricsForDpi(smCXSmIcon, a.primaryDPI())
	if size <= 0 {
		size = 16
	}
	if h := extractIcon(a.exePath, 0, size); h != 0 {
		return h
	}
	return genericIcon()
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
