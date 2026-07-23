//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type iconKey struct {
	file string
	idx  int32
	size int32
}

var (
	iconCache    = map[iconKey]syscall.Handle{}
	fallbackIcon syscall.Handle
)

// parseIconSpec splits "C:\path\app.exe,3" into its file and index. Paths may
// contain commas, so only a trailing ",<int>" counts as an index. A negative
// index means "resource ID", matching ExtractIcon's convention.
func parseIconSpec(spec string) (string, int32) {
	spec = strings.TrimSpace(spec)
	if i := strings.LastIndexByte(spec, ','); i > 0 {
		if n, err := strconv.ParseInt(strings.TrimSpace(spec[i+1:]), 10, 32); err == nil {
			return strings.TrimSpace(spec[:i]), int32(n)
		}
	}
	return spec, 0
}

// isAppsFolder reports whether s is a shell:AppsFolder moniker -- the icon
// source for a Store app, whose image comes from the shell image factory rather
// than from icon-resource extraction.
func isAppsFolder(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), strings.ToLower(appsFolderPrefix))
}

// urlScheme returns the scheme of a URL-ish string, or "" for a filesystem
// path. A drive letter ("C:\...") is deliberately not a scheme, hence the
// requirement that the scheme be longer than one character.
func urlScheme(s string) string {
	i := strings.IndexByte(s, ':')
	if i <= 1 {
		return ""
	}
	for _, r := range s[:i] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '+' || r == '-' || r == '.') {
			return ""
		}
	}
	return strings.ToLower(s[:i])
}

// iconContainers are the file types that carry icon resources of their own, so
// they are their own best icon source.
var iconContainers = map[string]bool{
	".exe": true, ".dll": true, ".ico": true, ".icl": true,
	".cpl": true, ".ocx": true, ".scr": true, ".drv": true,
}

// resolveIconSource decides which file and index hold an entry's icon.
//
// An explicit spec always wins. Otherwise the order matters, because the two
// obvious shell APIs each cover only half the cases:
//
//   - SHGetFileInfo(SHGFI_ICONLOCATION) is right for shortcuts and anything
//     that carries its own icon, but for a document it usually just points
//     back at the document, which holds nothing extractable.
//   - AssocQueryString(ASSOCSTR_DEFAULTICON) is where a document's icon
//     actually lives -- .xlsx resolves to xlicons.exe,1 and .txt to
//     imageres.dll,-102 -- but it knows nothing about a specific file.
//
// So: self-contained types first, then the shell, then the type association.
func resolveIconSource(exec, iconSpec string) (string, int32) {
	if iconSpec != "" {
		return parseIconSpec(iconSpec)
	}
	if exec == "" {
		return "", 0
	}

	// A Store app's moniker is kept verbatim as the icon source; iconFor routes
	// it to the shell image factory. It must be caught before urlScheme, which
	// would otherwise see "shell:" as a scheme and try a type association.
	if isAppsFolder(exec) {
		return exec, 0
	}

	if scheme := urlScheme(exec); scheme != "" {
		if file, idx, ok := assocDefaultIcon(scheme); ok {
			return file, idx
		}
		if exe := assocExecutable(scheme); exe != "" {
			return exe, 0
		}
		return "", 0
	}

	if iconContainers[strings.ToLower(filepath.Ext(exec))] {
		return exec, 0
	}

	// Only trust the shell when it names a file other than the target itself.
	shellFile, shellIdx, shellOK := shellIconLocation(exec)
	if shellOK && !samePath(shellFile, exec) {
		return shellFile, shellIdx
	}

	assoc := strings.ToLower(filepath.Ext(exec))
	if assoc == "" {
		assoc = "Folder" // no extension: most likely a directory
	}
	if file, idx, ok := assocDefaultIcon(assoc); ok {
		return file, idx
	}

	if shellOK {
		return shellFile, shellIdx
	}
	return exec, 0
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// iconFor returns a cached HICON at exactly size pixels, extracting it if
// necessary. Requesting the exact pixel size is what keeps icons crisp at
// 150% and 200% scaling instead of stretching a 16px frame.
func iconFor(file string, idx, size int32) syscall.Handle {
	if size <= 0 {
		size = 16
	}
	if file == "" {
		return genericIcon()
	}
	key := iconKey{file: file, idx: idx, size: size}
	if h, ok := iconCache[key]; ok {
		return h
	}

	var h syscall.Handle
	switch {
	case isAppsFolder(file):
		h = shellImageIcon(file, size)
	case isImageFile(file):
		h = imageIcon(file, size) // SVG/PNG/AVIF/... rendered through the imaging stack
	default:
		h = extractIcon(file, idx, size)
		if h == 0 && idx != 0 {
			h = extractIcon(file, 0, size) // requested index missing -- fall back to the first
		}
	}
	if h == 0 {
		h = genericIcon()
		iconCache[key] = h // negative-cache so we do not retry the extraction per show
		return h
	}
	iconCache[key] = h
	return h
}

// genericIcon is the shared system application icon. It is owned by the system
// and must never be passed to DestroyIcon, which is why purgeIcons skips it.
func genericIcon() syscall.Handle {
	if fallbackIcon == 0 {
		fallbackIcon = loadDefaultIcon()
	}
	return fallbackIcon
}

// purgeIcons drops every extracted icon. Called on config reload so that icons
// belonging to removed entries do not leak.
func purgeIcons() {
	for k, h := range iconCache {
		if h != 0 && h != fallbackIcon {
			destroyIcon(h)
		}
		delete(iconCache, k)
	}
}

// ---------------------------------------------------------------------------
// CLI helpers
// ---------------------------------------------------------------------------

// listIcons reports how many icons a file holds so an index can be chosen for
// a config entry. imageres.dll and shell32.dll carry hundreds.
func listIcons(path string) int {
	n := iconCount(path)
	if n <= 0 {
		fmt.Fprintf(os.Stderr, "%s: no icons found\n", path)
		return 1
	}
	fmt.Printf("%s\n", path)
	fmt.Printf("  icons            : %d\n", n)
	fmt.Printf("  usable indices   : 0-%d\n", n-1)
	// jsonEscape already doubles the backslashes; %q would double them again.
	fmt.Printf("  config spec      : \"%s,<index>\"\n", jsonEscape(path))
	fmt.Printf("\nAny index can be requested at any pixel size; the best frame is\n")
	fmt.Printf("selected and scaled per monitor DPI.\n")
	return 0
}

// pickIcon opens the standard Windows "Change Icon" dialog and prints a spec
// string ready to paste into config.json.
func pickIcon(path string) int {
	file, idx, ok := pickIconDlg(path, 0)
	if !ok {
		fmt.Fprintln(os.Stderr, "cancelled")
		return 1
	}
	fmt.Printf("%s,%d\n", file, idx)
	fmt.Printf("\nconfig.json:\n  \"icon\": \"%s,%d\"\n", jsonEscape(file), idx)
	return 0
}

// jsonEscape backslash-escapes a Windows path for pasting into JSON.
func jsonEscape(s string) string {
	return strings.ReplaceAll(s, `\`, `\\`)
}
