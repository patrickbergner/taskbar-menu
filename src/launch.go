//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const errorCancelled = syscall.Errno(1223) // ERROR_CANCELLED: user dismissed UAC

// appsFolderPrefix is the shell moniker that turns an AppUserModelID into
// something ShellExecuteEx can launch. A Microsoft Store (packaged) app has no
// .exe to point at, but ShellExecuteEx resolves "shell:AppsFolder\<AUMID>" to
// the app's activation the same way Start does.
const appsFolderPrefix = `shell:AppsFolder\`

// launchTarget is what ShellExecuteEx is handed as lpFile: the exec path, or a
// Store app's AppsFolder moniker built from its AppUserModelID.
func launchTarget(n *node) string {
	if n.appID != "" {
		return appsFolderPrefix + n.appID
	}
	return n.exec
}

// launch starts one menu entry.
//
// ShellExecuteExW covers everything a single entry can ask for: a null verb
// invokes the registered default handler, so an .xlsx opens in Excel, a folder
// in Explorer, an https:// URL in the browser and a Store app's AppsFolder
// moniker its activation, all with no special-casing here; "runas" elevates;
// nShow carries the window state; lpDirectory the working directory.
func launch(n *node) error {
	verb := (*uint16)(nil)
	if n.elevated {
		verb = utf16Ptr("runas")
	}
	target := launchTarget(n)
	file := utf16Ptr(target)
	params := (*uint16)(nil)
	if len(n.args) > 0 {
		params = utf16Ptr(quoteArgs(n.args))
	}
	dir := utf16Ptr(n.cwd)

	var sei SHELLEXECUTEINFOW
	sei.Size = uint32(unsafe.Sizeof(sei))
	sei.Mask = seeMaskNoAsync | seeMaskFlagNoUI
	sei.Verb = verb
	sei.File = file
	sei.Parameters = params
	sei.Directory = dir
	sei.Show = n.show

	ok, errno := shellExecute(&sei)

	runtime.KeepAlive(verb)
	runtime.KeepAlive(file)
	runtime.KeepAlive(params)
	runtime.KeepAlive(dir)

	switch {
	case ok:
		return nil
	case errno == errorCancelled:
		return nil // cancelling the UAC prompt is not a failure worth reporting
	default:
		return fmt.Errorf("%s: %w", target, errno)
	}
}

// quoteArgs joins arguments into a single command line.
//
// The array form in config.json exists so paths with spaces cannot be
// mis-escaped by hand; this is where that array becomes the string
// CommandLineToArgvW will split back into the same pieces.
func quoteArgs(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = quoteArg(a)
	}
	return strings.Join(parts, " ")
}

// quoteArg applies the CommandLineToArgvW escaping rules: backslashes are only
// special immediately before a quote, where they must be doubled.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); {
		slashes := 0
		for i < len(s) && s[i] == '\\' {
			slashes++
			i++
		}
		switch {
		case i == len(s):
			// Trailing backslashes would escape the closing quote.
			b.WriteString(strings.Repeat(`\`, slashes*2))
		case s[i] == '"':
			b.WriteString(strings.Repeat(`\`, slashes*2+1))
			b.WriteByte('"')
			i++
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
			b.WriteByte(s[i])
			i++
		}
	}
	b.WriteByte('"')
	return b.String()
}

// openPath hands a path to the shell with the default verb. Used for the
// "Edit config" command and the config-error item.
func openPath(path string) {
	p := utf16Ptr(path)
	var sei SHELLEXECUTEINFOW
	sei.Size = uint32(unsafe.Sizeof(sei))
	sei.Mask = seeMaskNoAsync | seeMaskFlagNoUI
	sei.File = p
	sei.Show = swShowNormal
	shellExecute(&sei)
	runtime.KeepAlive(p)
}
