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

// isShellMoniker reports whether s names something in the shell namespace rather
// than a file: a "shell:" moniker or a "::{CLSID}" path. Neither holds an icon
// resource to extract, and both are exactly what SHCreateItemFromParsingName
// takes -- so the image factory is the only route to their icon, and also the
// better one, because it yields the Recycle Bin's *current* full-or-empty icon
// rather than a frame frozen at config-load time.
//
// isAppsFolder stays separate: "is this a Store app" is a different question,
// asked by launch.go and shortcut.go for different reasons.
func isShellMoniker(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(l, "shell:") || strings.HasPrefix(l, "::{")
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

// isLinkFile reports whether s is a shell shortcut: the opposite of an icon
// container, a file that names an icon instead of holding one.
//
// .url is not one of these. An Internet shortcut cannot be read through
// IShellLink, and it does not need to be -- its registered association already
// resolves to the browser or to url.dll, which is what Explorer shows for it.
func isLinkFile(s string) bool {
	return strings.EqualFold(filepath.Ext(s), ".lnk")
}

// maxLinkHops bounds a chain of shortcuts pointing at shortcuts.
//
// The shell will not produce such a chain -- it collapses one as it writes it,
// storing the eventual target rather than the intermediate link. This is for the
// .lnk the shell did not write, where the bound is what turns a cycle into a
// generic icon instead of a hang.
const maxLinkHops = 4

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
// iconSourceCache memoizes the shell and association lookups below, which are
// the only expensive part of building a menu -- SHGetFileInfoW and
// AssocQueryStringW cost hundreds of microseconds each. That did not matter
// while the menu was built once per config change, but a config with dynamic
// submenus rebuilds on every show, where sixty entries would be the difference
// between 20us and 20ms. Cleared with the icons themselves on reload.
var iconSourceCache = map[[2]string]iconSource{}

// iconSourceCacheMax bounds it separately from iconCache. With lazy icons the
// two grow at very different rates: iconCache only gains an entry when an item
// is actually painted, while this one gains an entry for every path enumerated,
// painted or not. Browsing a deep tree over a long session would otherwise
// accumulate tens of thousands of full paths that will never be looked up again.
const iconSourceCacheMax = 4096

type iconSource struct {
	file string
	idx  int32
}

func resolveIconSource(exec, iconSpec string) (string, int32) {
	key := [2]string{exec, iconSpec}
	if s, ok := iconSourceCache[key]; ok {
		return s.file, s.idx
	}
	file, idx := resolveIconSourceUncached(exec, iconSpec)
	iconSourceCache[key] = iconSource{file: file, idx: idx}
	return file, idx
}

func resolveIconSourceUncached(exec, iconSpec string) (string, int32) {
	if iconSpec != "" {
		return parseIconSpec(iconSpec)
	}
	if exec == "" {
		return "", 0
	}

	// A shell moniker is kept verbatim as the icon source; iconFor routes it to
	// the shell image factory. It must be caught before urlScheme, which would
	// otherwise see "shell:" as a scheme and try a type association -- and find
	// one, since ms-settings: and friends do register a DefaultIcon that points
	// at a package resource the extractor cannot read.
	if isShellMoniker(exec) {
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

	// A shortcut names an icon rather than holding one, so it is followed to
	// whatever does hold it before anything below gets a look -- the target may
	// itself be an icon container, a folder or a document, and each of those is
	// already handled. Walked as a loop rather than by recursing because a link
	// may point at another link, and a cycle of them must not be a hang.
	for i := 0; isLinkFile(exec) && i < maxLinkHops; i++ {
		icon, idx, target := linkIconSource(exec)
		if icon != "" {
			return icon, idx
		}
		if target == "" || samePath(target, exec) {
			break
		}
		exec = target
	}
	// Still a shortcut: nothing extractable behind it, which is what a link onto
	// a Store app looks like. iconFor sends it to the shell image factory, so it
	// draws the package logo Explorer draws rather than the generic icon.
	if isLinkFile(exec) {
		return exec, 0
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
	// A shortcut only reaches here when resolveIconSource found nothing
	// extractable behind it, which is the same position a shell moniker is in:
	// the image factory is the only route to the icon, and it is the right one.
	case isShellMoniker(file), isLinkFile(file):
		h = shellImageIcon(file, size)
	case isImageFile(file):
		h = imageIcon(file, size) // SVG/PNG/AVIF/... rendered through the imaging stack
	default:
		h = extractIcon(file, idx, size)
		if h == 0 && idx != 0 {
			h = extractIcon(file, 0, size) // requested index missing -- fall back to the first
		}
		if h == 0 {
			// Nothing embedded, but the shell still knows what Explorer draws for
			// it. That is the difference between an icon and the generic square
			// for the two kinds of file that reach here: one that holds no icon
			// resources despite its extension promising some -- a .msc, or an
			// .exe like Ollama's that ships without any -- and a document whose
			// association gave nothing.
			h = shellImageIcon(file, size)
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
	clear(iconSourceCache)
}

// purgeCaches additionally drops what is merely expensive rather than scarce.
// Separate from purgeIcons because the two have different triggers: GDI handle
// pressure wants the icons gone and has no reason to throw away a shell listing
// that costs 100-300ms to rebuild, whereas a config reload wants everything
// re-read -- which is also what makes "Reload config" the answer to "I just
// installed something and it is not in the list".
func purgeCaches() {
	purgeIcons()
	purgeShellListings()
	purgeLocalizedNames()
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
