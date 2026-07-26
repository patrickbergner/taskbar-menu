//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Power actions are the one part of the catalog that cannot be expressed as an
// exec. Shelling out to shutdown.exe would work, but it spawns a console
// process, cannot report why it failed, and gets the reason code wrong -- so
// these go straight to ExitWindowsEx, LockWorkStation and SetSuspendState.

type powerOp int

const (
	opLock powerOp = iota
	opSignOut
	opRestart
	opShutdown
	opSleep
	opHibernate
)

// powerOps maps a catalog id to the operation it performs. Keyed by the same id
// the table uses, so there is one name for each concept rather than two.
var powerOps = map[string]powerOp{
	"lock":      opLock,
	"signOut":   opSignOut,
	"restart":   opRestart,
	"shutdown":  opShutdown,
	"sleep":     opSleep,
	"hibernate": opHibernate,
}

func powerOpFor(id string) (powerOp, bool) {
	op, ok := powerOps[id]
	return op, ok
}

// destructive reports whether the operation can lose unsaved work, which is
// what decides whether it confirms by default. Locking, sleeping and
// hibernating are all fully reversible and leave every application running, so
// prompting for them would be noise; signing out, restarting and shutting down
// close everything.
func (op powerOp) destructive() bool {
	return op == opSignOut || op == opRestart || op == opShutdown
}

func (op powerOp) prompt() string {
	switch op {
	case opSignOut:
		return ui.PowerPromptSignOut
	case opRestart:
		return ui.PowerPromptRestart
	case opShutdown:
		return ui.PowerPromptShutdown
	case opSleep:
		return ui.PowerPromptSleep
	case opHibernate:
		return ui.PowerPromptHibernate
	default:
		return ui.PowerPromptLock
	}
}

// verb names the operation for NotifyPowerFailedFormat ("Could not %s"), so
// a translated sentence reads naturally instead of leaking the internal
// catalog id ("shutdown") into it.
func (op powerOp) verb() string {
	switch op {
	case opSignOut:
		return ui.PowerVerbSignOut
	case opRestart:
		return ui.PowerVerbRestart
	case opShutdown:
		return ui.PowerVerbShutdown
	case opSleep:
		return ui.PowerVerbSleep
	case opHibernate:
		return ui.PowerVerbHibernate
	default:
		return ui.PowerVerbLock
	}
}

// powerExec is the single point at which this program can turn the machine off.
//
// It is a variable so a test can prove the confirmation gating and the flag
// selection without the test host signing out or rebooting. NO TEST MAY EVER
// CALL THE REAL IMPLEMENTATION -- substitute this and assert on the op.
var powerExec = realPowerExec

func realPowerExec(op powerOp) error {
	switch op {
	case opLock:
		// The only one of these that needs no privilege at all.
		if r, _, e := procLockWorkStation.Call(); r == 0 {
			return e
		}
		return nil

	case opSleep, opHibernate:
		if err := enablePrivilege(seShutdownName); err != nil {
			return err
		}
		hibernate := uintptr(0)
		if op == opHibernate {
			hibernate = 1
		}
		// SetSuspendState(Hibernate, Force, WakeupEventsDisabled). Force is 0 so
		// drivers may veto; with hibernation switched off in power settings
		// Windows silently sleeps instead, which is documented behaviour and not
		// something this can detect up front.
		if r, _, e := procSetSuspendState.Call(hibernate, 0, 0); r == 0 {
			return e
		}
		return nil

	default:
		// EWX_FORCEIFHUNG rather than EWX_FORCE: applications still get their
		// chance to save, but a single hung one cannot wedge the shutdown.
		flags := uintptr(ewxForceIfHung)
		switch op {
		case opSignOut:
			flags |= ewxLogoff
		case opRestart:
			flags |= ewxReboot
		default:
			flags |= ewxShutdown
		}
		// Logging off needs no privilege; powering the machine down does.
		if op != opSignOut {
			if err := enablePrivilege(seShutdownName); err != nil {
				return err
			}
		}
		if r, _, e := procExitWindowsEx.Call(flags, shtdnReasonPlanned); r == 0 {
			return e
		}
		return nil
	}
}

// enablePrivilege turns one privilege on in this process's token.
//
// Two traps, both of which produce a silent no-op rather than an error if
// missed. First, the privilege being *present* in the token is not enough --
// ExitWindowsEx fails with ERROR_ACCESS_DENIED until it is explicitly enabled.
// Second, AdjustTokenPrivileges returns TRUE even when it assigned nothing at
// all, so the ERROR_NOT_ALL_ASSIGNED check below is the only thing that
// actually reports a standard user lacking the right.
//
// Called lazily on first use, so a menu with no power entries never touches its
// own token.
func enablePrivilege(name string) error {
	var tok syscall.Handle
	self, _, _ := procGetCurrentProcess.Call()
	if r, _, e := procOpenProcessToken.Call(self,
		tokenAdjustPrivileges|tokenQuery, uintptr(unsafe.Pointer(&tok))); r == 0 {
		return fmt.Errorf("OpenProcessToken: %w", e)
	}
	defer syscall.CloseHandle(tok)

	var tp TOKEN_PRIVILEGES
	p := utf16Ptr(name)
	r, _, e := procLookupPrivilegeValueW.Call(0,
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&tp.Privileges[0].Luid)))
	if r == 0 {
		return fmt.Errorf("LookupPrivilegeValue(%s): %w", name, e)
	}
	tp.PrivilegeCount = 1
	tp.Privileges[0].Attributes = sePrivilegeEnabled

	r, _, e = procAdjustTokenPrivileges.Call(uintptr(tok), 0,
		uintptr(unsafe.Pointer(&tp)), 0, 0, 0)
	if r == 0 {
		return fmt.Errorf("AdjustTokenPrivileges: %w", e)
	}
	if errno, ok := e.(syscall.Errno); ok && errno == errorNotAllAssigned {
		return fmt.Errorf("%s is not held by this account", name)
	}
	return nil
}

// powerAction builds the node.action for a power special.
//
// The confirmation is raised from here, which invoke() calls only after track()
// has returned and torn the popup down. That ordering is not incidental: a
// modal dialog created while the menu loop still owns the mouse capture and the
// input queue is the classic way to end up with a dialog behind the menu and a
// menu that will not dismiss. Do not move this into a WM_COMMAND or
// WM_INITMENUPOPUP handler.
func (a *appState) powerAction(id string, confirm bool) func() {
	op, ok := powerOpFor(id)
	if !ok {
		return nil
	}
	return func() {
		if confirm && !a.confirmPower(op) {
			return
		}
		if err := powerExec(op); err != nil {
			a.notify(windowTitle, fmt.Sprintf(ui.NotifyPowerFailedFormat, op.verb(), err.Error()), niifError)
		}
	}
}

// confirmPower asks before something irreversible. MB_DEFBUTTON2 puts the focus
// on No, so a stray Enter does not shut the machine down.
func (a *appState) confirmPower(op powerOp) bool {
	return messageBoxEx(a.hwnd, windowTitle, op.prompt(),
		mbYesNo|mbIconWarning|mbDefButton2|mbSetForeground|mbTopMost) == idYes
}

// confirmDefault is whether an entry confirms when the config says nothing.
func confirmDefault(id string) bool {
	op, ok := powerOpFor(id)
	return ok && op.destructive()
}
