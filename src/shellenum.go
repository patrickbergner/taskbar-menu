//go:build windows

package main

import (
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// Enumerating a shell namespace folder, which is the only way to list things
// that are not directories: the AppsFolder holds Store apps that have no path
// at all, and Control Panel's applets are namespace items rather than files.
//
// Reached through the same hand-rolled COM as image.go and shortcut.go. Slot
// numbers are the method's position in the interface, counting the three
// IUnknown methods first.
const (
	shellItemBindToHandler  = 3 // IShellItem
	shellItemGetDisplayName = 5

	enumShellItemsNext = 3 // IEnumShellItems
)

// SIGDN forms. NORMALDISPLAY is what the user should read ("Windows Terminal");
// PARSINGNAME is what ShellExecuteEx and the image factory both accept back.
const (
	sigdnNormalDisplay = 0x00000000
	sigdnParsingName   = 0x80018001
)

var (
	// IID_IShellItem {43826D1E-E718-42EE-BC55-A1E261C37BFE}
	iidShellItem = GUID{0x43826D1E, 0xE718, 0x42EE,
		[8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
	// BHID_EnumItems {94F60519-2850-4924-AA5A-D15E84868039}
	bhidEnumItems = GUID{0x94F60519, 0x2850, 0x4924,
		[8]byte{0xAA, 0x5A, 0xD1, 0x5E, 0x84, 0x86, 0x80, 0x39}}
	// IID_IEnumShellItems {70629033-E363-4A28-A567-0DB78006E6D7}
	iidEnumShellItems = GUID{0x70629033, 0xE363, 0x4A28,
		[8]byte{0xA5, 0x67, 0x0D, 0xB7, 0x80, 0x06, 0xE6, 0xD7}}
)

// shellEntry is one child of a shell folder, reduced to what a menu needs. The
// COM objects are released before this crosses back out, so nothing
// apartment-bound escapes the enumeration.
type shellEntry struct {
	display string
	parsing string
}

// enumShellFolder lists a shell namespace folder named by its parsing name
// ("shell:AppsFolder", "shell:ControlPanelFolder", ...).
//
// BindToHandler(BHID_EnumItems) is the documented way to get an enumerator
// without dropping to raw PIDLs, which is what keeps this readable.
//
// The caller's thread must have CoInitialize'd; the server does at startup and
// the --launch shim does before it launches anything.
func enumShellFolder(parsingName string, limit int, b *walkBudget) []shellEntry {
	name := utf16Ptr(parsingName)
	if name == nil {
		return nil
	}
	var folder unsafe.Pointer
	hr, _, _ := procSHCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(name)), 0,
		uintptr(unsafe.Pointer(&iidShellItem)),
		uintptr(unsafe.Pointer(&folder)))
	runtime.KeepAlive(name)
	if hr != 0 || folder == nil {
		return nil
	}
	defer release(folder)

	var enum unsafe.Pointer
	if hr := comCall(folder, shellItemBindToHandler, 0,
		uintptr(unsafe.Pointer(&bhidEnumItems)),
		uintptr(unsafe.Pointer(&iidEnumShellItems)),
		uintptr(unsafe.Pointer(&enum))); hr != 0 || enum == nil {
		return nil
	}
	defer release(enum)

	out := make([]shellEntry, 0, 64)
	for len(out) < limit && !b.spent() {
		var item unsafe.Pointer
		var fetched uint32
		hr := comCall(enum, enumShellItemsNext, 1,
			uintptr(unsafe.Pointer(&item)), uintptr(unsafe.Pointer(&fetched)))
		if hr != 0 || fetched == 0 || item == nil {
			break // S_FALSE (1) is the normal end of the enumeration
		}
		e := shellEntry{
			display: shellDisplayName(item, sigdnNormalDisplay),
			parsing: shellDisplayName(item, sigdnParsingName),
		}
		release(item)
		b.take(1)
		if e.display == "" || e.parsing == "" || isUnnamedShellItem(e) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// isUnnamedShellItem reports whether an entry has no name of its own, which the
// shell signals by handing back a parsing name where a display name belongs.
//
// A stock Windows 11 Control Panel has exactly one: an item registered under
// {98F2AB62-0E29-4E4C-8EE7-B542E66740B1} with nothing behind the CLSID.
// Explorer leaves it out of the Control Panel window; the menu used to show it
// as a row reading "::{26EE0668-…}\0\::{98F2AB62-…}" that did nothing when
// clicked, since there is no applet there to open.
//
// Only the display name is examined. Comparing it against the entry's own
// parsing name would look like the stronger test, and is one this cannot use:
// the parsing name of a namespace child that is a real file or directory is
// simply its name on disk, which is also what it is called on screen.
func isUnnamedShellItem(e shellEntry) bool {
	return strings.HasPrefix(e.display, "::")
}

// shellDisplayName reads one name form off an IShellItem. The string is
// allocated by the shell and must go back to CoTaskMemFree.
func shellDisplayName(item unsafe.Pointer, form uintptr) string {
	var p *uint16
	if hr := comCall(item, shellItemGetDisplayName, form,
		uintptr(unsafe.Pointer(&p))); hr != 0 || p == nil {
		return ""
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	// A packaged app's parsing name runs well past the 256 units that suffice
	// for a settings-change name, and truncating one yields a target that fails
	// to launch rather than an obvious error.
	return utf16PtrToStringN(p, 32768)
}

// launchTargetFor makes a parsing name safe to hand to ShellExecuteEx and to
// the shell image factory.
//
// The name being converted is SIGDN_PARENTRELATIVEPARSING -- relative, as the
// constant says. Relative names have to be made absolute before anything can
// use them, and how depends on the parent, so each of the three shapes the
// listings produce gets its own answer:
//
//   - A file is already absolute. Note that This PC yields the bare "C:" rather
//     than "C:\", so the test has to accept a drive letter with nothing after it.
//   - A Control Panel applet names itself "::{CLSID}", which "shell:" makes
//     absolute.
//   - Anything else came out of the AppsFolder and is an AppUserModelID.
//
// Both prefixes are load-bearing, and neither was applied widely enough before.
// Packaged apps have an AUMID with no punctuation in it, so a rule that skipped
// anything containing a backslash or a colon happened to work for them -- but a
// *desktop* app in the AppsFolder gets a known-folder-relative AUMID instead,
// "{1AC14E77-…}\charmap.exe", and a Control Panel applet always has the colons.
// Both fell straight through. Forty entries of All Apps and every one of the
// Control Panel Items drew the generic icon, and clicking any of them reported
// that the file could not be found -- correctly, since as paths they do not
// exist.
func launchTargetFor(parsing string) string {
	l := strings.ToLower(parsing)
	switch {
	case parsing == "" || isFilesystemPath(parsing):
		return parsing
	case strings.HasPrefix(l, "shell:"):
		return parsing // already absolute
	case strings.HasPrefix(l, "::{"):
		return "shell:" + parsing
	default:
		return appsFolderPrefix + parsing
	}
}

// isFilesystemPath reports whether s is drive-qualified or UNC. A bare "C:"
// counts: that, not "C:\", is what the This PC listing returns for a drive, and
// reading it as an AUMID would prefix it into nonsense.
func isFilesystemPath(s string) bool {
	if strings.HasPrefix(s, `\\`) {
		return true
	}
	return len(s) >= 2 && s[1] == ':' &&
		(s[0] >= 'A' && s[0] <= 'Z' || s[0] >= 'a' && s[0] <= 'z')
}

// ---------------------------------------------------------------------------
// Localized file names
// ---------------------------------------------------------------------------

// A folder can rename what it holds, and Explorer shows the renamed form.
//
// The mechanism is desktop.ini: a [LocalizedFileNames] line maps a file name
// onto a string resource, and a folder's own LocalizedResourceName renames the
// folder itself. Windows Tools is where everybody meets it -- the folder ships
// "services.lnk" and "dfrgui.lnk" and Explorer lists them as "Services" and
// "Defragment and Optimize Drives" -- and the Start Menu tree has the folder
// case, where "Administrative Tools" reads as "Windows Tools". A menu that
// prints file names is visibly not the list the user knows.
//
// SHGetLocalizedName answers both questions, including for a language pack the
// file names know nothing about, and hands back a module and a string id.
func localizedName(path string) string {
	if s, ok := localizedNames[path]; ok {
		return s
	}
	s := localizedNameUncached(path)
	// One flat eviction rather than an LRU: the map only grows while dynamic
	// submenus are being browsed, and rebuilding an entry costs a millisecond.
	if len(localizedNames) >= localizedNameCacheMax {
		clear(localizedNames)
	}
	localizedNames[path] = s
	return s
}

// localizedNames memoizes the lookup, misses included. Both halves are slow
// enough to matter inside WM_INITMENUPOPUP -- SHGetLocalizedName reads
// desktop.ini and LoadStringW walks the MUI chain, together about a millisecond
// per name -- and a dynamic submenu is re-enumerated on every show, so without
// this the Windows Tools flyout would pay ~30ms every time it opened.
var localizedNames = map[string]string{}

const localizedNameCacheMax = 4096

func purgeLocalizedNames() { clear(localizedNames) }

func localizedNameUncached(path string) string {
	p := utf16Ptr(path)
	if p == nil {
		return ""
	}
	// Generous for a module path, because a desktop.ini may name one anywhere;
	// the call truncates into a load that fails rather than writing past this.
	mod := make([]uint16, 512)
	var id int32
	hr, _, _ := procSHGetLocalizedName.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&mod[0])), uintptr(len(mod)),
		uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(p)
	if hr != 0 {
		// E_INVALIDARG is the answer for "this path has no localized name",
		// which is the ordinary case for almost every file on the machine.
		return ""
	}
	return resourceString(expandEnv(utf16ToString(mod)), id)
}

// resourceString reads one string resource out of a module.
//
// The id arrives as desktop.ini writes it, which may carry the leading minus of
// "@shell32.dll,-21762". The minus marks a resource id rather than an index and
// is not part of the number, so it is dropped before LoadStringW, which reads
// its argument as unsigned and would otherwise look for resource 4294945534.
func resourceString(module string, id int32) string {
	h := resourceModule(module)
	if h == 0 {
		return ""
	}
	if id < 0 {
		id = -id
	}
	buf := make([]uint16, 512)
	n, _, _ := procLoadStringW.Call(uintptr(h), uintptr(uint32(id)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return utf16ToString(buf[:n])
}

// resourceModule maps a file for its resources alone and keeps the mapping.
//
// LOAD_LIBRARY_AS_DATAFILE means no code runs and no DllMain is called, which
// is what makes it safe to point this at whatever a desktop.ini names. The
// handles are deliberately never freed: one localized folder draws every one of
// its names out of the same one or two system modules, and mapping shell32.dll
// twenty times to build a single popup would cost far more than the map holding
// a handful of handles ever will.
var resourceModules = map[string]syscall.Handle{}

const loadLibraryAsDatafile = 0x2

func resourceModule(path string) syscall.Handle {
	if h, ok := resourceModules[path]; ok {
		return h
	}
	var h syscall.Handle
	if p := utf16Ptr(path); p != nil {
		r, _, _ := procLoadLibraryExW.Call(
			uintptr(unsafe.Pointer(p)), 0, loadLibraryAsDatafile)
		runtime.KeepAlive(p)
		h = syscall.Handle(r)
	}
	// A failure is cached as the zero handle: a module named by a desktop.ini
	// that is not on this machine must not be retried per entry per show.
	resourceModules[path] = h
	return h
}

// isDriveRoot reports whether a parsing name is a plain drive root like "C:\".
// Used to keep the This PC listing to actual drives -- it also contains the
// Documents/Pictures/... shortcuts, which belong in their own entries.
func isDriveRoot(parsing string) bool {
	s := strings.TrimSuffix(parsing, `\`)
	if len(s) != 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
