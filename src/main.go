//go:build windows

package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	// These are prefixes, not the final names. The window class and mutex that
	// identify a resident server are suffixed with a hash of the config path (see
	// instanceNames), so one menu per config file can run at the same time.
	classPrefix = "TaskbarMenuHost"
	mutexPrefix = `Local\TaskbarMenu.SingleInstance`
	windowTitle = "TaskbarMenu"

	wmShowMenu  = wmApp + 1
	wmTrayIcon  = wmApp + 2
	wmFixArrows = wmApp + 3
)

// version is stamped in at link time by build.cmd, which passes the contents of
// the VERSION file as -X main.version=... A plain `go build ./src` keeps the
// placeholder, so a hand-built exe never claims to be a release.
var version = "0.0.0-dev"

type appState struct {
	exePath   string
	cfgPath   string
	className string // per-config window class, so FindWindowW matches this config only
	cfg       *Config
	cfgErr    error
	cfgStamp  stamp

	hwnd  syscall.Handle
	hinst syscall.Handle

	// flat is indexed by dwItemData-1; byID maps command ids back to nodes.
	flat   []*node
	byID   map[uint32]*node
	lastID uint32

	menu     syscall.Handle
	trayMenu syscall.Handle

	builtDPI uint32
	metrics  metrics
	palette  palette
	font     syscall.Handle
	bgBrush  syscall.Handle

	trayAdded bool
	showing   bool

	taskbarCreatedMsg uint32
}

var app appState

// ---------------------------------------------------------------------------
// Diagnostics
//
// A tray app built with -H windowsgui has nowhere to print. Set
// TASKBARMENU_LOG=1 for %TEMP%\taskbarmenu.log, or to a path of your choosing.
// ---------------------------------------------------------------------------

var logFile *os.File

func initLog() {
	path := os.Getenv("TASKBARMENU_LOG")
	if path == "" {
		return
	}
	if path == "1" {
		path = filepath.Join(os.TempDir(), "taskbarmenu.log")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	logFile = f
	logf("--- pid %d, %v ---", os.Getpid(), os.Args)
}

func logf(format string, args ...any) {
	if logFile == nil {
		return
	}
	fmt.Fprintf(logFile, time.Now().Format("15:04:05.000")+" "+format+"\n", args...)
}

func main() {
	// Every Win32 UI call in this process must happen on one OS thread:
	// window creation, the message loop, TrackPopupMenuEx, GDI and
	// ShellExecuteEx. Pinning the main goroutine here is what guarantees that.
	runtime.LockOSThread()
	initLog()

	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		attachParentConsole()
		fmt.Fprintln(os.Stderr, err)
		usage()
		os.Exit(2)
	}

	switch {
	case opts.launch != "":
		// The shim a pinned shortcut runs: launch one target and exit without ever
		// creating a window, so this process never becomes its own taskbar button.
		// No console: it is invoked by a click, not from a shell.
		os.Exit(runLaunch(opts))
	case opts.makeLauncher != "" || opts.makeAll:
		attachParentConsole()
		os.Exit(makeLaunchers(opts))
	case opts.help:
		attachParentConsole()
		usage()
		return
	case opts.version:
		attachParentConsole()
		fmt.Printf("TaskbarMenu %s\n", version)
		return
	case opts.listIcons != "":
		attachParentConsole()
		os.Exit(listIcons(opts.listIcons))
	case opts.pickIcon != "":
		attachParentConsole()
		os.Exit(pickIcon(opts.pickIcon))
	case opts.check:
		attachParentConsole()
		os.Exit(checkConfig(defaultConfigPath(opts.cfgPath)))
	}

	// Per-Monitor V2 must be set before any window or DC exists. Doing it here
	// replaces an application manifest entirely, so the build needs no resource
	// tooling for DPI awareness.
	setProcessDPIAware()
	logf("dpi awareness set")

	// The instance is identified by the config file it serves, so a launch only
	// reuses a resident server when it targets the same config -- another config
	// starts its own server instead of hijacking the first one.
	cfgPath := defaultConfigPath(opts.cfgPath)
	class, mutex := instanceNames(cfgPath)
	logf("config: %s class: %s", cfgPath, class)

	// Fast path: a server is already resident for this config, so hand it the
	// cursor position and get out. This is the branch that runs on every taskbar
	// click, and it is why the menu feels instant.
	if signalExistingServer(class, mutex) {
		logf("signalled existing server, exiting")
		return
	}
	logf("becoming the server")

	runServer(opts, cfgPath, class)
	logf("server exited")
}

// instanceNames derives the window class and mutex names that identify the
// resident server for a particular config file. Two launches share an instance
// only when their config paths hash to the same key, which is what allows a
// separate menu per config file to be resident at once.
func instanceNames(cfgPath string) (class, mutex string) {
	// Windows paths are case-insensitive, so fold case before hashing; that way
	// C:\App\config.json and c:\app\CONFIG.JSON resolve to one instance rather
	// than two servers fighting over the same file.
	norm := strings.ToLower(filepath.Clean(cfgPath))
	h := fnv.New64a()
	h.Write([]byte(norm))
	key := strconv.FormatUint(h.Sum64(), 16)
	return classPrefix + "." + key, mutexPrefix + "." + key
}

// ---------------------------------------------------------------------------
// Command line
// ---------------------------------------------------------------------------

type options struct {
	cfgPath      string
	background   bool
	check        bool
	listIcons    string
	pickIcon     string
	launch       string // run the launcher entry with this id/label, then exit
	makeLauncher string // write a pinnable .lnk for this launcher id/label
	makeAll      bool   // write a .lnk for every launcher entry
	out          string // output directory for generated shortcuts
	help         bool
	version      bool
}

func parseArgs(args []string) (options, error) {
	var o options
	i := 0
	value := func(flag string) (string, error) {
		if i+1 >= len(args) {
			return "", fmt.Errorf("%s requires a value", flag)
		}
		i++
		return args[i], nil
	}
	for ; i < len(args); i++ {
		var err error
		switch a := args[i]; a {
		case "--config", "-c":
			o.cfgPath, err = value(a)
		case "--list-icons":
			o.listIcons, err = value(a)
		case "--pick-icon":
			o.pickIcon, err = value(a)
		case "--launch":
			o.launch, err = value(a)
		case "--make-launcher":
			o.makeLauncher, err = value(a)
		case "--make-launchers":
			o.makeAll = true
		case "--out":
			o.out, err = value(a)
		case "--background", "-b":
			o.background = true
		case "--check":
			o.check = true
		case "--help", "-h", "/?":
			o.help = true
		case "--version", "-v":
			o.version = true
		default:
			return o, fmt.Errorf("unknown argument %q", a)
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

func usage() {
	fmt.Printf(`TaskbarMenu %s — a pinned-taskbar popup menu driven by config.json

Usage:
  TaskbarMenu.exe [--config <path>] [--background]
  TaskbarMenu.exe --list-icons <file>
  TaskbarMenu.exe --pick-icon  <file>
  TaskbarMenu.exe --launch <id>
  TaskbarMenu.exe --make-launcher  <id> [--out <dir>]
  TaskbarMenu.exe --make-launchers [--out <dir>]

The first run loads the config and stays resident in the tray; every later run
just tells the resident instance to show the menu at the cursor and exits.

The "launchers" section of config.json defines standalone, pin-to-taskbar
entries. --launch runs one directly (this is what a pinned shortcut calls);
--make-launcher / --make-launchers write .lnk files (one per entry) that you
drag onto the taskbar. Each click starts a new instance and the launcher never
becomes a running-app entry, like the classic Windows launchers.

Options:
  -c, --config <path>     Config file (default: config.json next to the exe)
  -b, --background        Stay resident without showing the menu (use at logon)
      --check             Validate the config and list every entry, then exit
      --list-icons <f>    Report how many icons a .exe/.dll/.ico holds
      --pick-icon  <f>    Open the Windows icon picker, print a config spec
      --launch <id>       Start the launcher entry with this id (or label)
      --make-launcher <id>  Write a pinnable .lnk for one launcher entry
      --make-launchers    Write a .lnk for every launcher entry
      --out <dir>         Output directory for shortcuts (default: .\Launchers)
  -h, --help              This text
  -v, --version           Version
`, version)
}

// defaultConfigPath resolves the config location the same way the server does,
// so --check validates exactly the file that would be loaded.
func defaultConfigPath(override string) string {
	path := override
	if path == "" {
		if exe, err := os.Executable(); err == nil {
			path = filepath.Join(filepath.Dir(exe), "config.json")
		} else {
			path = "config.json"
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// checkConfig validates without launching anything and prints the menu that
// would be built, so a config can be proof-read before it is pinned.
func checkConfig(path string) int {
	fmt.Printf("%s\n\n", path)

	cfg, err := LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR  %v\n", err)
		return 1
	}

	items, subs, seps := printItems(cfg.Items, "  ")
	fmt.Printf("\n%d entr(ies), %d submenu(s), %d separator(s)\n", items, subs, seps)

	if len(cfg.Launchers) > 0 {
		fmt.Printf("\nlaunchers:\n")
		for _, l := range cfg.Launchers {
			target := l.Exec
			if l.AppID != "" {
				target = "appId: " + l.AppID
			}
			fmt.Printf("  %-24s %s\n", l.Id, target)
		}
		fmt.Printf("\n%d launcher(s)\n", len(cfg.Launchers))
	}

	if len(cfg.Warnings) > 0 {
		fmt.Printf("\n%d warning(s):\n", len(cfg.Warnings))
		for _, w := range cfg.Warnings {
			fmt.Printf("  ! %s\n", w)
		}
		return 1
	}
	fmt.Println("\nOK")
	return 0
}

// printItems renders the tree and reports what could not be resolved. Missing
// targets are warnings, not errors: a path on a drive that is not mounted right
// now is still a valid entry.
func printItems(items []Item, indent string) (leaves, subs, seps int) {
	for _, it := range items {
		switch {
		case it.Type == "separator":
			fmt.Printf("%s----\n", indent)
			seps++
		case len(it.Items) > 0:
			fmt.Printf("%s%s  >\n", indent, it.Label)
			subs++
			l, s, p := printItems(it.Items, indent+"    ")
			leaves, subs, seps = leaves+l, subs+s, seps+p
		default:
			// A Store app is named by its AppUserModelID rather than a path;
			// there is nothing on disk to stat, so it is reported as-is.
			target := it.Exec
			note := ""
			if it.AppID != "" {
				target = "appId: " + it.AppID
			} else if urlScheme(it.Exec) == "" {
				if _, err := os.Stat(it.Exec); err != nil {
					if searchPath(it.Exec) == "" {
						note = "   [target not found]"
					}
				}
			}
			iconExec := it.Exec
			if it.AppID != "" {
				iconExec = appsFolderPrefix + it.AppID
			}
			file, idx := resolveIconSource(iconExec, it.Icon)
			icon := "no icon"
			switch {
			case isAppsFolder(file):
				icon = "app icon"
			case file != "":
				icon = fmt.Sprintf("%s,%d", filepath.Base(file), idx)
			}
			fmt.Printf("%s%-28s %-24s %s%s\n", indent, it.Label, "("+icon+")", target, note)
			leaves++
		}
	}
	return
}

// ---------------------------------------------------------------------------
// Single instance
// ---------------------------------------------------------------------------

// signalExistingServer posts to an already-running instance if there is one.
//
// AllowSetForegroundWindow matters here: this process was started by a user
// click and therefore holds foreground privilege, while the resident server
// does not. Handing the privilege over is what lets the server's menu take
// focus -- and therefore dismiss itself when the user clicks elsewhere.
func signalExistingServer(class, mutex string) bool {
	pt := getCursorPos()

	if hwnd := findWindow(class); hwnd != 0 {
		return poke(hwnd, pt)
	}

	// No window yet. Another instance may still be starting up, so take the
	// mutex to decide who becomes the server.
	name := utf16Ptr(mutex)
	h, _, errno := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	runtime.KeepAlive(name)
	if h == 0 {
		return false
	}
	if e, ok := errno.(syscall.Errno); !ok || e != syscall.ERROR_ALREADY_EXISTS {
		return false // we own the mutex: become the server
	}

	// Someone else is mid-startup; give them a moment to publish the window.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hwnd := findWindow(class); hwnd != 0 {
			return poke(hwnd, pt)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false // gave up waiting: become the server ourselves
}

func poke(hwnd syscall.Handle, pt POINT) bool {
	allowSetForegroundWindow(getWindowProcessID(hwnd))
	// Coordinates are packed as two int32s: negative values are normal on
	// multi-monitor layouts, so the round trip has to be sign-preserving.
	return postMessage(hwnd, wmShowMenu, uintptr(uint32(pt.X)), uintptr(uint32(pt.Y)))
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

func runServer(opts options, cfgPath, class string) {
	a := &app
	a.byID = map[uint32]*node{}
	a.cfg = defaultConfig()

	exe, err := os.Executable()
	if err != nil {
		messageBox(windowTitle, "cannot determine own path: "+err.Error(), mbOK|mbIconError)
		return
	}
	a.exePath = exe
	a.cfgPath = cfgPath
	a.className = class

	coInitialize()
	logf("config: %s", a.cfgPath)

	if !a.createHostWindow() {
		messageBox(windowTitle, "cannot create host window", mbOK|mbIconError)
		return
	}
	logf("host window hwnd=%#x hinst=%#x", a.hwnd, a.hinst)

	a.taskbarCreatedMsg = registerWindowMessage("TaskbarCreated")

	a.reload(true)
	logf("config loaded: err=%v items=%d warnings=%d", a.cfgErr, len(a.cfg.Items), len(a.cfg.Warnings))

	a.addTrayIcon()
	logf("tray icon added=%v", a.trayAdded)

	if !opts.background {
		a.showMenu(getCursorPos())
	}

	logf("entering message loop")
	a.messageLoop()
	a.shutdown()
}

// createHostWindow makes a hidden top-level window.
//
// Deliberately not a message-only window: those are invisible to FindWindowW
// and cannot become the foreground window, both of which this design depends
// on. WS_POPUP without WS_VISIBLE plus WS_EX_TOOLWINDOW keeps it off the
// taskbar and out of Alt-Tab while remaining a real top-level window.
func (a *appState) createHostWindow() bool {
	a.hinst = getModuleHandle()

	cls := utf16Ptr(a.className)
	var wc WNDCLASSEXW
	wc.Size = uint32(unsafe.Sizeof(wc))
	wc.WndProc = syscall.NewCallback(wndProc)
	wc.Instance = a.hinst
	wc.ClassName = cls
	if registerClassEx(&wc) == 0 {
		runtime.KeepAlive(cls)
		return false
	}
	runtime.KeepAlive(cls)

	a.hwnd = createWindowEx(wsExToolWindow, a.className, windowTitle, wsPopup,
		0, 0, 0, 0, 0, 0, a.hinst)
	return a.hwnd != 0
}

func (a *appState) messageLoop() {
	var msg MSG
	for {
		r := getMessage(&msg)
		if r <= 0 {
			return
		}
		translateMessage(&msg)
		dispatchMessage(&msg)
	}
}

func (a *appState) shutdown() {
	a.removeTrayIcon()
	destroyMenu(a.menu)
	destroyMenu(a.trayMenu)
	deleteObject(a.font)
	deleteObject(a.bgBrush)
	purgeIcons()
	if a.hwnd != 0 {
		destroyWindow(a.hwnd)
	}
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	a := &app
	switch uint32(msg) {
	case wmShowMenu:
		a.showMenu(POINT{X: int32(uint32(wp)), Y: int32(uint32(lp))})
		return 0

	case wmTrayIcon:
		a.onTrayMessage(lp)
		return 0

	case wmMeasureItem:
		if a.onMeasureItem(winPtr[MEASUREITEMSTRUCT](lp)) {
			return 1
		}

	case wmDrawItem:
		if a.onDrawItem(winPtr[DRAWITEMSTRUCT](lp)) {
			return 1
		}

	case wmFixArrows:
		// Posted by onDrawItem once a submenu item's redraw cycle -- including
		// Windows' own follow-up arrow paint -- has fully finished. Every open
		// popup is re-checked rather than just the one that triggered this,
		// since it is one cheap pass and avoids tracking which window a given
		// WM_DRAWITEM belonged to.
		for h := range borderOrigProcs {
			paintSubmenuArrows(h)
		}
		return 0

	case wmSettingChange:
		if lp != 0 && utf16PtrToString(winPtr[uint16](lp)) == "ImmersiveColorSet" {
			a.builtDPI = 0 // force a rebuild so colours and brushes are re-made
		}
		return 0

	case wmClose, wmDestroy:
		postQuitMessage(0)
		return 0
	}

	if a.taskbarCreatedMsg != 0 && uint32(msg) == a.taskbarCreatedMsg {
		// Explorer restarted and dropped every notification icon.
		a.trayAdded = false
		a.addTrayIcon()
		return 0
	}

	return defWindowProc(hwnd, msg, wp, lp)
}

// ---------------------------------------------------------------------------
// Showing the menu
// ---------------------------------------------------------------------------

func (a *appState) showMenu(cursor POINT) {
	if a.showing {
		return // TrackPopupMenuEx is modal and pumps messages; do not re-enter
	}
	a.reload(false)

	p := resolvePlacement(cursor, a.cfg.Anchor)
	a.ensureBuilt(p.dpi)
	a.invoke(a.track(a.menu, p))
}

func (a *appState) showTrayMenu(cursor POINT) {
	if a.showing {
		return
	}
	a.reload(false)
	p := resolvePlacement(cursor, a.cfg.Anchor)
	a.ensureBuilt(p.dpi)
	a.invoke(a.track(a.trayMenu, p))
}

// track displays a menu and returns the chosen command id.
//
// The selected command is deliberately *not* invoked here. track holds the
// re-entrancy guard, and some commands (the tray's "Show menu", "Reload
// config") need to open a menu or tear down the HMENU -- neither of which is
// safe or possible while the guard is held and the popup is still on screen.
func (a *appState) track(menu syscall.Handle, p placement) uint32 {
	if menu == 0 {
		return 0
	}
	a.showing = true
	defer func() { a.showing = false }()

	// Park the invisible owner on the target monitor so the menu window Windows
	// creates for us inherits the right DPI.
	moveWindowTo(a.hwnd, p.x, p.y)
	setForegroundWindow(a.hwnd)

	if a.palette == darkPalette {
		installBorderHook(a.palette.sep)
		defer uninstallBorderHook()
	}

	var tpm TPMPARAMS
	tpm.Size = uint32(unsafe.Sizeof(tpm))
	tpm.RcExclude = p.exclude

	flags := p.flags | tpmReturnCmd | tpmNoNotify | tpmLeftButton | tpmWorkArea
	cmd := trackPopupMenuEx(menu, flags, p.x, p.y, a.hwnd, &tpm)

	// The other half of the classic dismissal fix: without this the menu can
	// linger after a click outside it.
	postMessage(a.hwnd, wmNull, 0, 0)
	return cmd
}

func (a *appState) invoke(id uint32) {
	if id == 0 {
		return
	}
	n := a.byID[id]
	if n == nil {
		return
	}
	if n.action != nil {
		n.action()
		return
	}
	if err := launch(n); err != nil {
		a.notify("TaskbarMenu", "Could not start "+n.label+"\n"+err.Error(), niifError)
	}
}

// ---------------------------------------------------------------------------
// Config + menu construction
// ---------------------------------------------------------------------------

// reload re-reads config.json when it has changed on disk. Stat-and-compare on
// every show costs about 50us and cannot miss an edit, which is why there is no
// file watcher here.
func (a *appState) reload(force bool) {
	st := statStamp(a.cfgPath)
	if !force && st.equal(a.cfgStamp) {
		return
	}
	a.cfgStamp = st

	cfg, err := LoadConfig(a.cfgPath)
	if err != nil {
		a.cfgErr = err
		a.cfg = defaultConfig()
	} else {
		a.cfgErr = nil
		a.cfg = cfg
	}

	purgeIcons()
	a.builtDPI = 0 // force rebuild on next show
	a.refreshTrayTip()
}

// reloadInteractive is the tray/error-menu "Reload" command. Unlike the silent
// reload on every show, this one reports what happened -- otherwise the command
// looks like it did nothing, since the rebuilt menu only appears on the next
// click.
func (a *appState) reloadInteractive() {
	a.reload(true)
	switch {
	case a.cfgErr != nil:
		a.notify(windowTitle, "Config error: "+a.cfgErr.Error(), niifError)
	case len(a.cfg.Warnings) > 0:
		a.notify(windowTitle, fmt.Sprintf("Reloaded %s with %d warning(s)",
			filepath.Base(a.cfgPath), len(a.cfg.Warnings)), niifWarning)
	default:
		a.notify(windowTitle, "Reloaded "+filepath.Base(a.cfgPath), niifInfo)
	}
}

// ensureBuilt rebuilds the HMENU when the DPI changed or the config was
// reloaded. The DPI check is not an optimisation detail: WM_MEASUREITEM is only
// sent the first time a menu is displayed, so a menu built at 96 DPI keeps its
// old item sizes on a 192 DPI monitor unless it is rebuilt.
func (a *appState) ensureBuilt(dpi uint32) {
	if a.builtDPI == dpi && a.menu != 0 {
		return
	}

	destroyMenu(a.menu)
	destroyMenu(a.trayMenu)
	a.menu, a.trayMenu = 0, 0
	a.flat = nil
	a.byID = map[uint32]*node{}
	a.lastID = 0

	a.palette = resolvePalette(a.cfg.Theme)
	a.metrics = computeMetrics(dpi, a.cfg.IconSize)

	deleteObject(a.font)
	a.font = menuFont(dpi)

	deleteObject(a.bgBrush)
	a.bgBrush = createSolidBrush(a.palette.bg)

	var nodes []*node
	if a.cfgErr != nil {
		nodes = a.errorNodes(a.cfgErr.Error())
	} else {
		nodes = a.buildNodes(a.cfg.Items)
		if len(a.cfg.Warnings) > 0 {
			warn := a.warningNode(len(a.cfg.Warnings))
			sep := &node{separator: true, disabled: true}
			a.register(sep)
			nodes = append([]*node{warn, sep}, nodes...)
		}
		if len(nodes) == 0 {
			nodes = a.errorNodes("no entries in " + filepath.Base(a.cfgPath))
		}
	}

	a.resolveIcons(nodes)
	a.menu = a.createMenu(nodes)
	a.applyMenuBackground(a.menu)

	tray := a.trayNodes()
	a.resolveIcons(tray)
	a.trayMenu = a.createMenu(tray)
	a.applyMenuBackground(a.trayMenu)

	a.builtDPI = dpi
}

func (a *appState) openConfig() { openPath(a.cfgPath) }

func (a *appState) primaryDPI() uint32 {
	return getDpiForMonitor(monitorFromPoint(POINT{}, monitorDefaultToNearest))
}

// winPtr reinterprets a pointer Windows passed us in a WPARAM/LPARAM.
//
// A window procedure receives its arguments as uintptr because that is the only
// shape syscall.NewCallback accepts, so recovering the pointer is unavoidable.
// The double indirection through &lp rather than a direct
// unsafe.Pointer(lp) conversion is what keeps `go vet -unsafeptr` quiet, and it
// is sound here for a specific reason: every pointer reaching this function
// addresses memory owned by the OS (a MEASUREITEMSTRUCT on the system stack, a
// string in a system DLL), never the Go heap. There is nothing for the garbage
// collector to track or move, and the pointee outlives the message handler.
func winPtr[T any](lp uintptr) *T {
	return (*T)(*(*unsafe.Pointer)(unsafe.Pointer(&lp)))
}

// utf16PtrToString reads a NUL-terminated UTF-16 string of unknown length, used
// for the WM_SETTINGCHANGE payload.
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	const max = 256
	buf := make([]uint16, 0, 32)
	for i := 0; i < max; i++ {
		c := *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + uintptr(i)*2))
		if c == 0 {
			break
		}
		buf = append(buf, c)
	}
	return syscall.UTF16ToString(buf)
}
