//go:build windows

// Launcher shortcuts. --make-launcher / --make-launchers turn the flat
// "launchers" section of config.json into .lnk files that run
// TaskbarMenu.exe --launch <id> and can be pinned to the taskbar.
//
// Two Windows facts drive the design:
//
//   - Multiple shortcuts to the *same* exe collapse into one taskbar button
//     unless each carries a distinct AppUserModelID; arguments do not
//     disambiguate. Since every launcher points at the one shared TaskbarMenu.exe,
//     each .lnk is stamped with a unique AUMID via IPropertyStore.
//   - The AUMID is what Windows keys a pinned button to, so it must be *stable*
//     across regenerations. aumid() derives it purely from the entry id, letting
//     --make-launchers be re-run any number of times without orphaning pins.
//
// Everything here reaches COM by hand through comCall, the same technique
// image.go uses; slot numbers count the three IUnknown methods plus every
// inherited method ahead of the one being called.
package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unsafe"
)

// makeLaunchers implements both --make-launcher <id> and --make-launchers. It
// loads the config, resolves the target set, and writes one .lnk per entry into
// the output directory, overwriting any that already exist so a rerun refreshes
// them in place.
func makeLaunchers(opts options) int {
	coInitialize()

	cfgPath := defaultConfigPath(opts.cfgPath)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR  %v\n", err)
		return 1
	}

	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR  cannot determine own path: %v\n", err)
		return 1
	}

	dir := opts.out
	if dir == "" {
		dir = filepath.Join(filepath.Dir(exePath), "Launchers")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR  %v\n", err)
		return 1
	}

	var targets []Launcher
	if opts.makeAll {
		targets = cfg.Launchers
	} else if l, ok := findLauncher(cfg, opts.makeLauncher); ok {
		targets = []Launcher{l}
	} else {
		fmt.Fprintf(os.Stderr, "ERROR  no launcher %q in %s\n", opts.makeLauncher, cfgPath)
		return 1
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "ERROR  no launchers defined in %s\n", cfgPath)
		return 1
	}

	// A non-default config has to travel into the shortcut so the pinned button
	// launches against the same file the user is editing.
	configArg := ""
	if opts.cfgPath != "" {
		configArg = cfgPath
	}

	fmt.Printf("%s\n\n", dir)
	written := 0
	for _, l := range targets {
		path, err := writeShortcut(exePath, configArg, l, dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %-24s ERROR  %v\n", l.Id, err)
			continue
		}
		fmt.Printf("  %s\n", filepath.Base(path))
		written++
	}

	fmt.Printf("\n%d shortcut(s) written. Drag them onto the taskbar to pin.\n", written)
	if written == 0 {
		return 1
	}
	return 0
}

// writeShortcut builds one .lnk: target = TaskbarMenu.exe, arguments =
// --launch <id>, the entry's own icon, and a unique, stable AUMID. Returns the
// path written.
func writeShortcut(exePath, configArg string, l Launcher, dir string) (string, error) {
	link := coCreateInstance(&clsidShellLink, &iidShellLinkW)
	if link == nil {
		return "", fmt.Errorf("CoCreateInstance(ShellLink) failed")
	}
	defer release(link)

	setLinkString(link, shlSetPath, exePath)

	args := "--launch " + quoteArg(l.Id)
	if configArg != "" {
		args = "--config " + quoteArg(configArg) + " " + args
	}
	setLinkString(link, shlSetArguments, args)

	if l.Cwd != "" {
		setLinkString(link, shlSetWorkingDirectory, l.Cwd)
	}

	if file, idx, ok := shortcutIcon(l); ok {
		p := utf16Ptr(file)
		comCall(link, shlSetIconLocation, uintptr(unsafe.Pointer(p)), uintptr(idx))
		runtime.KeepAlive(p)
	}

	// The distinct AUMID is what keeps the pins from merging into one button.
	if ps := comQI(link, &iidPropertyStore); ps != nil {
		defer release(ps)
		setAppUserModelID(ps, aumid(l.Id))
	}

	pf := comQI(link, &iidPersistFile)
	if pf == nil {
		return "", fmt.Errorf("IPersistFile unavailable")
	}
	defer release(pf)

	out := filepath.Join(dir, sanitizeFileName(l.Id)+".lnk")
	p := utf16Ptr(out)
	hr := comCall(pf, persistFileSave, uintptr(unsafe.Pointer(p)), 1) // fRemember = TRUE
	runtime.KeepAlive(p)
	if hr != 0 {
		return "", fmt.Errorf("Save failed hr=%#x", hr)
	}
	return out, nil
}

// setLinkString calls one of IShellLinkW's Set* methods with a single wide
// string argument, keeping the buffer alive across the call.
func setLinkString(link unsafe.Pointer, slot int, s string) {
	p := utf16Ptr(s)
	comCall(link, slot, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}

// setAppUserModelID stamps System.AppUserModel.ID onto the link's property
// store. The store deep-copies the string on SetValue, so the buffer only has to
// survive that one call.
func setAppUserModelID(ps unsafe.Pointer, id string) {
	p := utf16Ptr(id)
	pv := propVariant{vt: vtLPWSTR, val: uintptr(unsafe.Pointer(p))}
	comCall(ps, propStoreSetValue, uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(&pv)))
	comCall(ps, propStoreCommit)
	runtime.KeepAlive(p)
	runtime.KeepAlive(pv)
}

// shortcutIcon picks the file+index for the .lnk icon. A shortcut icon location
// must be an exe/dll/ico + index, so an entry whose icon is an image file (SVG,
// PNG) or a Store-app logo can't be used directly -- fall back to the target's
// own extractable icon, or none (the exe's icon then shows).
func shortcutIcon(l Launcher) (string, int32, bool) {
	iconExec := l.Exec
	if l.AppID != "" {
		iconExec = appsFolderPrefix + l.AppID
	}
	file, idx := resolveIconSource(iconExec, l.Icon)
	if usableShortcutIcon(file) {
		return file, idx, true
	}
	// Retry ignoring an explicit image/store icon: resolve from the target itself.
	file, idx = resolveIconSource(iconExec, "")
	if usableShortcutIcon(file) {
		return file, idx, true
	}
	return "", 0, false
}

// usableShortcutIcon rejects the sources a .lnk cannot express. A shell moniker
// is one of them for the same reason a Store logo is: IShellLink::SetIconLocation
// takes a file plus an index, and neither a "shell:" path nor an image file is
// one. Another .lnk is rejected on the same grounds -- resolveIconSource returns
// one only when there is nothing extractable behind it, so pointing at it would
// write a shortcut with a blank icon.
func usableShortcutIcon(file string) bool {
	return file != "" && !isImageFile(file) && !isShellMoniker(file) && !isLinkFile(file)
}

// ---------------------------------------------------------------------------
// Reading a shortcut
// ---------------------------------------------------------------------------

// linkIconSource reads where a .lnk's icon comes from: the explicit icon
// location the shortcut stores, if it has one, and its target, which is where
// the icon comes from when it does not.
//
// It lives here, beside the writer, because this is where IShellLink is already
// understood -- but the reason it exists is icons.go's. A .lnk holds no icon
// resources, so it has to be followed to something that does, and
// SHGetFileInfo(SHGFI_ICONLOCATION) will not do it: on a plain Start Menu it
// answers for the Office links and returns nothing at all for Firefox,
// Thunderbird, VS Code or Chromium. Chromium's link does store an icon
// location, so the shell is not passing on an absence -- it simply declines.
// Reading the link answers both halves directly, and cannot decline.
//
// Load-only, never Resolve: Resolve goes looking for a target that has moved,
// which for a link onto a disconnected share means blocking the menu's modal
// loop until the SMB client gives up.
func linkIconSource(path string) (icon string, idx int32, target string) {
	link := coCreateInstance(&clsidShellLink, &iidShellLinkW)
	if link == nil {
		return "", 0, ""
	}
	defer release(link)

	pf := comQI(link, &iidPersistFile)
	if pf == nil {
		return "", 0, ""
	}
	defer release(pf)

	p := utf16Ptr(path)
	hr := comCall(pf, persistFileLoad, uintptr(unsafe.Pointer(p)), stgmRead)
	runtime.KeepAlive(p)
	if hr != 0 {
		return "", 0, ""
	}

	// One buffer for both calls: utf16ToString copies, so the second read is
	// free to overwrite what the first returned.
	buf := make([]uint16, 1024)
	if comCall(link, shlGetIconLocation, uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)), uintptr(unsafe.Pointer(&idx))) == 0 {
		icon = expandEnv(utf16ToString(buf))
	}
	if icon == "" {
		idx = 0 // a failed GetIconLocation may still have written the index
	}
	// GetPath yields nothing for a link onto a non-filesystem target -- a Store
	// app, a Control Panel item -- which is an ordinary shape in the Start Menu
	// rather than an error. The caller falls back to the shell image factory.
	if comCall(link, shlGetPath, uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)), 0, 0) == 0 {
		target = utf16ToString(buf)
	}
	runtime.KeepAlive(buf)
	return icon, idx, target
}

// aumid derives a launcher's AppUserModelID from its id and nothing else, so it
// is byte-identical every time the shortcut is regenerated. Folding in the
// label, path, a random GUID or a timestamp would drift the value and strand any
// existing pin.
//
// The result stays inside the format limits (<=128 total, each dot-section
// <=64): the token is a single dot-free section capped at 48 characters; an id
// that would overflow is truncated and given a stable hash suffix so length caps
// can never make two ids collide.
func aumid(id string) string {
	const prefix = "TaskbarMenu.Launcher."
	token := sanitizeAUMID(id)
	if token == "" {
		token = "_"
	}
	if len(token) > 48 {
		h := fnv.New32a()
		h.Write([]byte(id))
		token = token[:48] + "_" + strconv.FormatUint(uint64(h.Sum32()), 16)
	}
	return prefix + token
}

// sanitizeAUMID maps an id to one dot-free AUMID section: every character
// outside [A-Za-z0-9] becomes '_'. Deterministic -- the same id always yields
// the same token.
func sanitizeAUMID(id string) string {
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// sanitizeFileName makes an id safe as a .lnk basename by replacing the
// characters Windows forbids in file names.
func sanitizeFileName(id string) string {
	name := strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, id)
	name = strings.TrimSpace(name)
	if name == "" {
		return "launcher"
	}
	return name
}

// ---------------------------------------------------------------------------
// COM vtable slots
// ---------------------------------------------------------------------------

const (
	// IShellLinkW (after the three IUnknown slots).
	shlGetPath             = 3
	shlSetWorkingDirectory = 9
	shlSetArguments        = 11
	shlGetIconLocation     = 16
	shlSetIconLocation     = 17
	shlSetPath             = 20

	// IPersistFile: IUnknown(0-2), IPersist::GetClassID(3), IsDirty(4), Load(5),
	// Save(6).
	persistFileLoad = 5
	persistFileSave = 6

	// IPropertyStore: IUnknown(0-2), GetCount(3), GetAt(4), GetValue(5),
	// SetValue(6), Commit(7).
	propStoreSetValue = 6
	propStoreCommit   = 7
)

// vtLPWSTR is VT_LPWSTR: the PROPVARIANT holds a pointer to a wide string.
const vtLPWSTR = 31

// propVariant is a PROPVARIANT reduced to the fields the VT_LPWSTR case needs:
// the type tag and the value pointer. The trailing word pads the struct to the
// 24-byte x64 size so nothing beyond it is read.
type propVariant struct {
	vt uint16
	_  uint16
	_  uint16
	_  uint16
	val uintptr
	_   uintptr
}

// propertyKey is PROPERTYKEY: a format GUID plus a property id.
type propertyKey struct {
	fmtid GUID
	pid   uint32
}

var (
	// CLSID_ShellLink {00021401-0000-0000-C000-000000000046}
	clsidShellLink = GUID{0x00021401, 0x0000, 0x0000,
		[8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	// IID_IShellLinkW {000214F9-0000-0000-C000-000000000046}
	iidShellLinkW = GUID{0x000214F9, 0x0000, 0x0000,
		[8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	// IID_IPersistFile {0000010B-0000-0000-C000-000000000046}
	iidPersistFile = GUID{0x0000010B, 0x0000, 0x0000,
		[8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	// IID_IPropertyStore {886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99}
	iidPropertyStore = GUID{0x886D8EEB, 0x8CF2, 0x4446,
		[8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}
	// PKEY_AppUserModel_ID: fmtid {9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3}, pid 5.
	pkeyAppUserModelID = propertyKey{
		fmtid: GUID{0x9F4C2855, 0x9F79, 0x4B39,
			[8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}},
		pid: 5,
	}
)
