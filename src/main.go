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

	// dynMenus maps a dynamic submenu's HMENU back to the node that produced it.
	// WM_INITMENUPOPUP identifies the popup by handle and nothing else --
	// dwItemData is an item property, and the popup being initialised is not an
	// item. Rebuilt with the menu, so it can never name a destroyed handle.
	dynMenus map[syscall.Handle]*node

	menu     syscall.Handle
	trayMenu syscall.Handle

	builtDPI uint32
	// menuDirty says the contents are stale while the measurements are still
	// good -- a dynamic config on every show. Kept apart from builtDPI so that
	// "rebuild the item tree" and "the DPI changed" stay different questions;
	// overloading builtDPI = 0 for both made a font and brush recreation the
	// price of a fresh directory listing.
	menuDirty bool
	metrics   metrics
	palette   palette
	font      syscall.Handle
	bgBrush   syscall.Handle

	// work is the work area of the monitor the menu was last built for, and the
	// height the column split is budgeted against. Part of what ensureBuilt
	// caches on, because two monitors can share a DPI and still differ here --
	// only one of them has the taskbar.
	work RECT

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
	case opts.listSpecials:
		attachParentConsole()
		os.Exit(listSpecials())
	case opts.check:
		attachParentConsole()
		os.Exit(checkConfig(defaultConfigPath(opts.cfgPath)))
	case opts.format:
		attachParentConsole()
		os.Exit(formatConfig(defaultConfigPath(opts.cfgPath)))
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
	listSpecials bool
	format       bool   // pretty-print the config file in place, then exit
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
		case "--list-specials":
			o.listSpecials = true
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
		case "--config-check":
			o.check = true
		case "--config-format":
			o.format = true
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
  TaskbarMenu.exe --list-specials
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

All options:
  -c, --config <path>     Config file (default: config.json next to the exe)
  -b, --background        Stay resident without showing the menu (use at logon)
      --config-check      Validate the config and list every entry, then exit
      --config-format     Pretty-print the config file in place, then exit
      --list-icons <f>    Report how many icons a .exe/.dll/.ico holds
      --pick-icon  <f>    Open the Windows icon picker, print a config spec
      --list-specials     List the built-in "special" entry ids
      --launch <id>       Start the launcher entry with this id (or label)
      --make-launcher <id>  Write a pinnable .lnk for one launcher entry
      --make-launchers    Write a .lnk for every launcher entry
      --out <dir>         Output directory for shortcuts (default: .\Launchers)
  -h, --help              This text
  -v, --version           Version
`, version)
}

// defaultConfigPath resolves the config location the same way the server does,
// so --config-check validates exactly the file that would be loaded.
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
		dyn := dynSourceFor(it)
		switch {
		case it.Type == "separator":
			fmt.Printf("%s----\n", indent)
			seps++
		case len(it.Items) > 0:
			note := ""
			if it.OpenAll {
				note = "  [open all]"
			}
			fmt.Printf("%s%s  >%s\n", indent, it.Label, note)
			subs++
			l, s, p := printItems(it.Items, indent+"    ")
			leaves, subs, seps = leaves+l, subs+s, seps+p
		case dyn != nil:
			// Contents are discovered when the menu is opened, so there is
			// nothing to list here -- report where they will come from instead.
			fmt.Printf("%s%s  >  (%s)\n", indent, it.Label, dynSummary(dyn))
			subs++
		default:
			// A Store app is named by its AppUserModelID rather than a path;
			// there is nothing on disk to stat, so it is reported as-is. A
			// surviving special is a built-in with no target at all.
			target := it.Exec
			note := ""
			if it.Special != "" {
				target = "built-in: " + it.Special
				if it.confirms() {
					note = "   [confirms]"
				}
			} else if it.AppID != "" {
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
			case isShellMoniker(file):
				icon = "shell icon"
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

	case wmInitMenuPopup:
		// HIWORD(lParam) is 1 for the window menu, which is never one of ours.
		if uint16(lp>>16) == 0 {
			a.onInitMenuPopup(syscall.Handle(wp))
		}
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
	a.refreshDynamic()

	p := resolvePlacement(cursor, a.cfg.Anchor)
	a.ensureBuilt(p.dpi, p.work)
	a.invoke(a.track(a.menu, p))
}

// iconCacheMax bounds the icons a long-lived session accumulates. A process has
// a 10,000 GDI object budget, and browsing dynamic submenus mints an icon per
// file seen; exhausting it makes the menu lose its icons and then stop drawing
// at all, which is a baffling failure to debug months later.
const iconCacheMax = 1024

// refreshDynamic discards the cached menu when the config enumerates anything,
// so each show sees the current contents of the directories it names.
//
// The rebuild is not the expense it looks like: ensureBuilt already runs after
// every config change, resolveIconSource is memoized and the extracted icons are
// cached, so what is repeated is allocation and one InsertMenuItemW per static
// entry. A config with no dynamic entries keeps the cached menu exactly as
// before.
func (a *appState) refreshDynamic() {
	if a.cfg == nil || !a.cfg.HasDynamic {
		return
	}
	// Purging is only safe while no menu is on screen and one is about to be
	// rebuilt -- under a cached menu it would leave live nodes holding freed
	// icon handles. Only the GDI handles go: the shell listings are not what the
	// cap is about, and dropping them would make the next "All Apps" pay the
	// full enumeration again for an unrelated reason.
	if len(iconCache) > iconCacheMax || len(iconSourceCache) > iconSourceCacheMax {
		purgeIcons()
	}
	a.menuDirty = true
}

func (a *appState) showTrayMenu(cursor POINT) {
	if a.showing {
		return
	}
	a.reload(false)
	p := resolvePlacement(cursor, a.cfg.Anchor)
	a.ensureBuilt(p.dpi, p.work)
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

	// tpmNoNotify is deliberately not set. It suppresses WM_INITMENUPOPUP, which
	// is what dynamic submenus are populated from -- measured on Windows 11,
	// where the message never arrives with the flag present. Dropping it is
	// safe: tpmReturnCmd already means the chosen id comes back from this call
	// instead of arriving as a WM_COMMAND, so nothing is invoked twice. The only
	// other messages it lets through are WM_ENTERMENULOOP/WM_EXITMENULOOP and
	// WM_MENUSELECT, none of which this window handles.
	flags := p.flags | tpmReturnCmd | tpmLeftButton | tpmWorkArea
	cmd := trackPopupMenuEx(menu, flags, p.x, p.y, a.hwnd, &tpm)

	// The other half of the classic dismissal fix: without this the menu can
	// linger after a click outside it.
	postMessage(a.hwnd, wmNull, 0, 0)
	return cmd
}

// onInitMenuPopup fills a dynamic submenu the first time it is opened.
//
// Windows sends this synchronously from the menu's modal loop, just before the
// popup is shown, and items inserted here are measured and drawn like any other
// -- which is the whole reason lazy population is possible at all. It is also
// why this must stay quick: the menu is frozen for its duration and there is no
// way to show progress inside a popup.
//
// Note that tpmNoNotify is deliberately absent from the flags in track(): it
// suppresses this message entirely.
func (a *appState) onInitMenuPopup(menu syscall.Handle) {
	n := a.dynMenus[menu]
	if n == nil || n.populated || n.dyn == nil {
		return
	}
	n.populated = true

	start := time.Now()
	n.children = a.expand(n.dyn)
	a.resolveIcons(n.children)
	a.fillMenu(menu, n.children)
	logf("populated %q: %d entries in %v", n.label, len(n.children), time.Since(start))

	// applyMenuBackground on the root only reached the submenus that existed
	// when it ran. A second level created just now missed it, and would draw a
	// light frame in dark mode.
	a.applyMenuBackground(menu)
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

	purgeCaches()
	a.builtDPI = 0 // force a full rebuild, measurements included
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

// ensureBuilt rebuilds the HMENU when the DPI or the work area changed, or the
// config was reloaded. The DPI check is not an optimisation detail:
// WM_MEASUREITEM is only sent the first time a menu is displayed, so a menu
// built at 96 DPI keeps its old item sizes on a 192 DPI monitor unless it is
// rebuilt. The work area is checked for the same reason one step removed -- the
// column breaks are baked into the items at insert time, so a menu split for a
// 1080p monitor keeps that split on a taller one until it is rebuilt.
func (a *appState) ensureBuilt(dpi uint32, work RECT) {
	if a.builtDPI == dpi && a.work == work && a.menu != 0 && !a.menuDirty {
		return
	}
	started := time.Now()
	a.work = work // fillMenu budgets the column split against this

	// Two different reasons to be here, with very different costs. A DPI or
	// theme change invalidates the palette, the font and the brushes; merely
	// wanting fresh dynamic contents does not. Telling them apart keeps three
	// registry reads, an SPI_GETNONCLIENTMETRICS, a font creation and a full
	// rebuild of the (never dynamic) tray menu off the every-click path.
	remeasure := a.builtDPI != dpi || a.font == 0

	destroyMenu(a.menu)
	a.menu = 0
	if remeasure {
		destroyMenu(a.trayMenu)
		a.trayMenu = 0
	}
	a.flat = nil
	a.byID = map[uint32]*node{}
	a.dynMenus = map[syscall.Handle]*node{}
	a.lastID = 0

	if remeasure {
		a.palette = resolvePalette(a.cfg.Theme)
		a.metrics = computeMetrics(dpi, a.cfg.IconSize)

		deleteObject(a.font)
		a.font = menuFont(dpi)

		deleteObject(a.bgBrush)
		a.bgBrush = createSolidBrush(a.palette.bg)
	}

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

	// The tray menu is fixed content, so it only needs rebuilding when the
	// metrics it was measured at changed. Its nodes still have to be registered
	// every time though, because the flat slice they index into was just reset.
	tray := a.trayNodes()
	a.resolveIcons(tray)
	if a.trayMenu == 0 {
		a.trayMenu = a.createMenu(tray)
		a.applyMenuBackground(a.trayMenu)
	} else {
		a.fillMenu(a.trayMenu, tray)
	}

	a.builtDPI = dpi
	a.menuDirty = false
	logf("menu built: dpi=%d entries=%d remeasure=%v dynamic=%v took=%v",
		dpi, len(nodes), remeasure, a.cfg.HasDynamic, time.Since(started))
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
func utf16PtrToString(p *uint16) string { return utf16PtrToStringN(p, 256) }

// utf16PtrToStringN is the same read with an explicit ceiling. The cap is a
// guard against a pointer that is not in fact NUL-terminated, so it has to suit
// the caller: 256 units is plenty for a settings-change name but would silently
// truncate a packaged app's parsing name into a target that fails to launch.
func utf16PtrToStringN(p *uint16, max int) string {
	if p == nil {
		return ""
	}
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
