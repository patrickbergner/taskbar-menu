//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
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

// launcherNode adapts a config Launcher to the node launch() consumes. Only the
// launch-relevant fields matter here -- no icon extraction or command id, since
// nothing is drawn.
func launcherNode(l Launcher) *node {
	n := &node{
		exec:     l.Exec,
		appID:    l.AppID,
		args:     l.Args,
		cwd:      l.Cwd,
		elevated: l.Elevated,
		show:     l.showCmd(),
	}
	// A power launcher has no target to hand ShellExecuteEx; it carries the
	// built-in instead, which runLaunch calls directly. app is the zero
	// appState here -- that is correct, since the shim has no window to own the
	// confirmation dialog.
	if l.Special != "" {
		n.action = app.powerAction(l.Special, l.confirms())
	}
	return n
}

// runLaunch is the --launch shim. It loads the config, resolves the launcher by
// id (or label), starts it, and returns an exit code. It creates no window and
// returns immediately, which is what keeps the pinned shortcut from turning into
// a running-app taskbar button -- the same behaviour the old VB launchers got
// from being windowless winexe stubs, now native on AMD64 and ARM64.
//
// It runs silently: this path is triggered by a taskbar click, so failures go to
// the TASKBARMENU_LOG sink rather than a console or a dialog.
func runLaunch(opts options) int {
	coInitialize() // ShellExecuteEx on a shell:AppsFolder target needs COM up
	cfg, err := LoadConfig(defaultConfigPath(opts.cfgPath))
	if err != nil {
		logf("launch: %v", err)
		return 1
	}
	if exe, err := os.Executable(); err == nil {
		ui = resolveLanguage(cfg.Language, filepath.Dir(exe)).strings
	}
	l, ok := findLauncher(cfg, opts.launch)
	if !ok {
		logf("launch: no launcher %q", opts.launch)
		return 1
	}
	n := launcherNode(l)
	// A built-in never reaches ShellExecuteEx. It may still put a confirmation
	// dialog on screen, which is fine from this windowless shim: it is a plain
	// modal with no owner, and no menu is up to fight it for the input queue.
	if n.action != nil {
		n.action()
		return 0
	}
	if err := launch(n); err != nil {
		logf("launch %q: %v", opts.launch, err)
		return 1
	}
	return 0
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
