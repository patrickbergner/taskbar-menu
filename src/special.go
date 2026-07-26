package main

import (
	"fmt"
	"strings"
)

// A "special" is a third way to name a target, alongside exec and appId: a short
// stable id standing for something Windows ships and everybody wants, paired
// with a curated label and icon.
//
// The target is rarely the interesting part -- most of these are one short
// string a user could have typed into exec themselves. What cannot be got right
// by hand is the icon. Windows registers no usable icon for a .msc at all: the
// association hands back the literal "%1" placeholder, meaning "the file
// itself", and a .msc file holds no icon resources, so every Management Console
// entry falls through to the generic application icon. The specs below are
// lifted from the shortcuts Windows ships in its own Tools folder wherever one
// exists, which is the same place Explorer reads them from.
type specialKind int

const (
	kindExec   specialKind = iota // preset over the existing exec path
	kindAction                    // built-in command; see power.go
	kindDyn                       // dynamic submenu; see dynamic.go
)

// specialEntry is one row of the catalog. An entry is data only: kindExec rows
// are desugared into exec/args/icon during normalisation and are indistinguish-
// able from a hand-written entry from that point on.
type specialEntry struct {
	id     string
	group  string // for --list-specials only
	label  string
	kind   specialKind
	target string
	args   []string
	icon   string // "" lets resolveIconSource decide, which is right for containers

	// roots is what a kindDyn entry enumerates: a directory (or directories --
	// the Start Menu really is two trees Explorer shows as one), or a single
	// shell parsing name.
	roots      []string
	dynKind    dynKind
	drivesOnly bool
}

// sys32 keeps the table readable. %SystemRoot% rather than a literal C:\Windows
// because Windows does not have to be installed on C:, and expandEnv already
// resolves it.
const sys32 = `%SystemRoot%\System32\`

// specials is the catalog, in the order --list-specials prints it.
//
// An icon is omitted wherever the target is already its own best source: .cpl
// and .exe files are icon containers, so resolveIconSource returns them
// unchanged and an explicit spec would only be a chance to be wrong.
var specials = []specialEntry{
	// --- Management -------------------------------------------------------
	{id: "deviceManager", group: "Management", label: "Device Manager",
		// -201, not index 0: index 0 of devmgr.dll is the driver-signing badge.
		// This is the id the shell class registration itself points at.
		target: sys32 + "devmgmt.msc", icon: sys32 + "devmgr.dll,-201"},
	{id: "diskManagement", group: "Management", label: "Disk Management",
		target: sys32 + "diskmgmt.msc", icon: sys32 + "dmdskres.dll,0"},
	{id: "computerManagement", group: "Management", label: "Computer Management",
		target: sys32 + "compmgmt.msc", icon: sys32 + "Mycomput.dll,2"},
	{id: "services", group: "Management", label: "Services",
		target: sys32 + "services.msc", icon: sys32 + "filemgmt.dll,0"},
	{id: "taskScheduler", group: "Management", label: "Task Scheduler",
		target: sys32 + "taskschd.msc", icon: sys32 + "miguiresource.dll,1"},
	{id: "eventViewer", group: "Management", label: "Event Viewer",
		target: sys32 + "eventvwr.msc", icon: sys32 + "miguiresource.dll,0"},
	{id: "gpedit", group: "Management", label: "Local Group Policy Editor",
		target: sys32 + "gpedit.msc", icon: sys32 + "gpedit.dll,0"},
	// filemgmt.dll holds no users icon -- index 0 there is the Services gears,
	// which is what this entry drew before anyone looked at it.
	{id: "localUsers", group: "Management", label: "Local Users and Groups",
		target: sys32 + "lusrmgr.msc", icon: sys32 + "netplwiz.exe,0"},
	{id: "secpol", group: "Management", label: "Local Security Policy",
		target: sys32 + "secpol.msc", icon: sys32 + "wsecedit.dll,0"},
	{id: "firewall", group: "Management", label: "Windows Defender Firewall",
		target: sys32 + "WF.msc", icon: sys32 + "AuthFWGP.dll,-101"},
	{id: "certificates", group: "Management", label: "Certificates",
		target: sys32 + "certmgr.msc", icon: sys32 + "certmgr.dll,0"},
	{id: "perfmon", group: "Management", label: "Performance Monitor",
		target: sys32 + "perfmon.msc", icon: sys32 + "wdc.dll,-108"},
	{id: "resourceMonitor", group: "Management", label: "Resource Monitor",
		target: sys32 + "perfmon.exe", args: []string{"/res"}, icon: sys32 + "wdc.dll,-108"},
	{id: "taskManager", group: "Management", label: "Task Manager",
		target: sys32 + "taskmgr.exe"},
	{id: "regedit", group: "Management", label: "Registry Editor",
		target: `%SystemRoot%\regedit.exe`, icon: `%SystemRoot%\regedit.exe,-100`},
	{id: "msconfig", group: "Management", label: "System Configuration",
		target: sys32 + "msconfig.exe", icon: sys32 + "msconfig.exe,-3000"},
	{id: "systemInfo", group: "Management", label: "System Information",
		target: sys32 + "msinfo32.exe"},
	// rundll32 holds no icon resources of its own, so this one genuinely needs a
	// spec: the dialog belongs to System Properties, hence sysdm.cpl.
	{id: "environmentVariables", group: "Management", label: "Environment Variables",
		target: sys32 + "rundll32.exe", args: []string{"sysdm.cpl,EditEnvironmentVariables"},
		icon: sys32 + "sysdm.cpl,0"},
	{id: "diskCleanup", group: "Management", label: "Disk Cleanup",
		target: sys32 + "cleanmgr.exe"},
	{id: "defrag", group: "Management", label: "Optimise Drives",
		target: sys32 + "dfrgui.exe"},
	{id: "mmc", group: "Management", label: "Microsoft Management Console",
		target: sys32 + "mmc.exe"},

	// --- Control Panel ----------------------------------------------------
	// Every .cpl is its own icon container, so none of these carry a spec.
	{id: "controlPanelHome", group: "Control Panel", label: "Control Panel",
		target: sys32 + "control.exe"},
	{id: "systemProperties", group: "Control Panel", label: "System Properties",
		target: sys32 + "sysdm.cpl"},
	{id: "networkConnections", group: "Control Panel", label: "Network Connections",
		target: sys32 + "ncpa.cpl"},
	{id: "programsAndFeatures", group: "Control Panel", label: "Programs and Features",
		target: sys32 + "appwiz.cpl"},
	{id: "displayControl", group: "Control Panel", label: "Display",
		target: sys32 + "desk.cpl"},
	{id: "mouse", group: "Control Panel", label: "Mouse",
		target: sys32 + "main.cpl"},
	{id: "soundControl", group: "Control Panel", label: "Sound",
		target: sys32 + "mmsys.cpl"},
	{id: "internetOptions", group: "Control Panel", label: "Internet Options",
		target: sys32 + "inetcpl.cpl"},
	{id: "dateTime", group: "Control Panel", label: "Date and Time",
		target: sys32 + "timedate.cpl"},
	{id: "securityMaintenance", group: "Control Panel", label: "Security and Maintenance",
		target: sys32 + "wscui.cpl"},
	// The one .cpl here that needs a spec: index 0 of bthprops.cpl is a warning
	// triangle. -200 of DevicePairingFolder.dll is what the Bluetooth Devices
	// shell class registers.
	{id: "bluetoothControl", group: "Control Panel", label: "Bluetooth",
		target: sys32 + "bthprops.cpl", icon: sys32 + "DevicePairingFolder.dll,-200"},
	{id: "userAccounts", group: "Control Panel", label: "User Accounts",
		target: sys32 + "netplwiz.exe"},

	// --- Settings ---------------------------------------------------------
	// One shared icon for every page, deliberately. The association for
	// ms-settings: resolves to a package resource path the icon extractor
	// cannot read, and resource -10 of SystemSettings.exe is what Windows' own
	// Win+X shortcuts point at. Per-page icons can be added later without a
	// schema change.
	{id: "settings", group: "Settings", label: "Settings",
		target: "ms-settings:", icon: settingsIcon},
	{id: "windowsUpdate", group: "Settings", label: "Windows Update",
		target: "ms-settings:windowsupdate", icon: settingsIcon},
	{id: "installedApps", group: "Settings", label: "Installed Apps",
		target: "ms-settings:appsfeatures", icon: settingsIcon},
	{id: "displaySettings", group: "Settings", label: "Display",
		target: "ms-settings:display", icon: settingsIcon},
	{id: "soundSettings", group: "Settings", label: "Sound",
		target: "ms-settings:sound", icon: settingsIcon},
	{id: "bluetoothDevices", group: "Settings", label: "Bluetooth and Devices",
		target: "ms-settings:bluetooth", icon: settingsIcon},
	{id: "networkSettings", group: "Settings", label: "Network and Internet",
		target: "ms-settings:network", icon: settingsIcon},
	{id: "defaultApps", group: "Settings", label: "Default Apps",
		target: "ms-settings:defaultapps", icon: settingsIcon},
	{id: "powerSettings", group: "Settings", label: "Power and Sleep",
		target: "ms-settings:powersleep", icon: settingsIcon},
	{id: "storage", group: "Settings", label: "Storage",
		target: "ms-settings:storagesense", icon: settingsIcon},
	{id: "about", group: "Settings", label: "About This PC",
		target: "ms-settings:about", icon: settingsIcon},
	{id: "printers", group: "Settings", label: "Printers and Scanners",
		target: "ms-settings:printers", icon: settingsIcon},

	// --- Places -----------------------------------------------------------
	// Shell monikers carry no extractable resource, so their icon comes from
	// the shell image factory instead; see isShellMoniker in icons.go. That is
	// not a workaround but the better answer: it gives the Recycle Bin its
	// current full-or-empty icon rather than a fixed frame.
	{id: "recycleBin", group: "Places", label: "Recycle Bin",
		target: "shell:RecycleBinFolder"},
	{id: "thisPC", group: "Places", label: "This PC",
		target: "shell:MyComputerFolder"},
	{id: "userProfile", group: "Places", label: "User Profile",
		target: "%USERPROFILE%"},
	{id: "networkFolder", group: "Places", label: "Network",
		target: "shell:NetworkPlacesFolder"},
	{id: "fonts", group: "Places", label: "Fonts",
		target: "shell:Fonts"},
	{id: "startup", group: "Places", label: "Startup Folder",
		target: "shell:Startup"},
	{id: "sendTo", group: "Places", label: "Send To Folder",
		target: "shell:SendTo"},
	{id: "temp", group: "Places", label: "Temp Folder",
		target: "%TEMP%"},

	// --- Folders (dynamic submenus) ----------------------------------------
	// Enumerated when opened; see dynamic.go. Icons come from the folder itself.
	{id: "startMenu", group: "Folders", label: "Start Menu", kind: kindDyn, roots: []string{
		`%APPDATA%\Microsoft\Windows\Start Menu\Programs`,
		`%ProgramData%\Microsoft\Windows\Start Menu\Programs`,
	}},
	{id: "windowsTools", group: "Folders", label: "Windows Tools", kind: kindDyn, roots: []string{
		`%ProgramData%\Microsoft\Windows\Start Menu\Programs\Administrative Tools`,
	}},
	{id: "desktop", group: "Folders", label: "Desktop", kind: kindDyn, roots: []string{
		`%USERPROFILE%\Desktop`,
		`%PUBLIC%\Desktop`,
	}},
	{id: "documents", group: "Folders", label: "Documents", kind: kindDyn,
		roots: []string{`%USERPROFILE%\Documents`}},
	{id: "downloads", group: "Folders", label: "Downloads", kind: kindDyn,
		roots: []string{`%USERPROFILE%\Downloads`}},
	{id: "pictures", group: "Folders", label: "Pictures", kind: kindDyn,
		roots: []string{`%USERPROFILE%\Pictures`}},

	// --- Shell namespace (dynamic submenus) --------------------------------
	// Listed through IEnumShellItems rather than the filesystem, because none of
	// these are directories: a Store app has no path at all, and a Control Panel
	// applet is a namespace item.
	{id: "allApps", group: "Shell", label: "All Apps", kind: kindDyn,
		dynKind: dynShell, roots: []string{"shell:AppsFolder"},
		icon: sys32 + "shell32.dll,-16"},
	{id: "drives", group: "Shell", label: "Drives", kind: kindDyn,
		dynKind: dynShell, roots: []string{"shell:MyComputerFolder"}, drivesOnly: true,
		icon: sys32 + "imageres.dll,-30"},
	{id: "controlPanel", group: "Shell", label: "Control Panel Items", kind: kindDyn,
		dynKind: dynShell, roots: []string{"shell:ControlPanelFolder"},
		icon: sys32 + "control.exe,0"},
	{id: "allSettings", group: "Shell", label: "All Settings", kind: kindDyn,
		dynKind: dynSettings, icon: settingsIcon},

	// --- Power -------------------------------------------------------------
	// These are the only entries that are not a target at all; see power.go.
	// The icons come from shell32, which is where the classic Shut Down dialog
	// has always kept them.
	{id: "lock", group: "Power", label: "Lock", kind: kindAction,
		icon: sys32 + "shell32.dll,47"},
	{id: "signOut", group: "Power", label: "Sign Out", kind: kindAction,
		icon: sys32 + "imageres.dll,209"},
	{id: "sleep", group: "Power", label: "Sleep", kind: kindAction,
		icon: sys32 + "shell32.dll,25"},
	// Hibernate deliberately shares Sleep's glyph: Windows ships no distinct
	// hibernate icon, and inventing a different one would imply a distinction
	// the system itself does not draw.
	{id: "hibernate", group: "Power", label: "Hibernate", kind: kindAction,
		icon: sys32 + "shell32.dll,25"},
	{id: "restart", group: "Power", label: "Restart", kind: kindAction,
		icon: sys32 + "imageres.dll,229"},
	{id: "shutdown", group: "Power", label: "Shut Down", kind: kindAction,
		icon: sys32 + "shell32.dll,27"},
}

// settingsIcon is resource -10 of the Settings app, the same spec Windows' own
// Win+X shortcuts use for every ms-settings: page.
const settingsIcon = `%SystemRoot%\ImmersiveControlPanel\SystemSettings.exe,-10`

// settingsPage is one entry of the allSettings submenu.
//
// This list is hand-maintained, and that is not laziness: there is no
// enumeration API for ms-settings: pages. They are URI handlers registered by
// the Settings app package, and nothing lists them --
// shell:ControlPanelFolder gives the *classic* applets, which are a different
// set. The drift is graceful: a URI a given Windows build does not know opens
// the Settings home page rather than failing, so an outdated entry is a mild
// annoyance and never a broken menu.
type settingsPage struct{ group, label, uri string }

var settingsPages = []settingsPage{
	{"System", "Display", "ms-settings:display"},
	{"System", "Sound", "ms-settings:sound"},
	{"System", "Notifications", "ms-settings:notifications"},
	{"System", "Power and Battery", "ms-settings:powersleep"},
	{"System", "Storage", "ms-settings:storagesense"},
	{"System", "Multitasking", "ms-settings:multitasking"},
	{"System", "Clipboard", "ms-settings:clipboard"},
	{"System", "About", "ms-settings:about"},

	{"Devices", "Bluetooth and Devices", "ms-settings:bluetooth"},
	{"Devices", "Printers and Scanners", "ms-settings:printers"},
	{"Devices", "Mouse", "ms-settings:mousetouchpad"},
	{"Devices", "Touchpad", "ms-settings:devices-touchpad"},
	{"Devices", "AutoPlay", "ms-settings:autoplay"},
	{"Devices", "USB", "ms-settings:usb"},

	{"Network", "Status", "ms-settings:network-status"},
	{"Network", "Wi-Fi", "ms-settings:network-wifi"},
	{"Network", "Ethernet", "ms-settings:network-ethernet"},
	{"Network", "VPN", "ms-settings:network-vpn"},
	{"Network", "Mobile Hotspot", "ms-settings:network-mobilehotspot"},
	{"Network", "Proxy", "ms-settings:network-proxy"},

	{"Personalisation", "Background", "ms-settings:personalization-background"},
	{"Personalisation", "Colours", "ms-settings:personalization-colors"},
	{"Personalisation", "Themes", "ms-settings:themes"},
	{"Personalisation", "Lock Screen", "ms-settings:lockscreen"},
	{"Personalisation", "Taskbar", "ms-settings:taskbar"},
	{"Personalisation", "Start", "ms-settings:personalization-start"},
	{"Personalisation", "Fonts", "ms-settings:fonts"},

	{"Apps", "Installed Apps", "ms-settings:appsfeatures"},
	{"Apps", "Default Apps", "ms-settings:defaultapps"},
	{"Apps", "Startup Apps", "ms-settings:startupapps"},
	{"Apps", "Optional Features", "ms-settings:optionalfeatures"},

	{"Accounts", "Your Info", "ms-settings:yourinfo"},
	{"Accounts", "Sign-in Options", "ms-settings:signinoptions"},
	{"Accounts", "Other Users", "ms-settings:otherusers"},
	{"Accounts", "Windows Backup", "ms-settings:backup"},

	{"Time and Language", "Date and Time", "ms-settings:dateandtime"},
	{"Time and Language", "Language and Region", "ms-settings:regionlanguage"},
	{"Time and Language", "Typing", "ms-settings:typing"},

	{"Privacy and Security", "Windows Update", "ms-settings:windowsupdate"},
	{"Privacy and Security", "Windows Security", "ms-settings:windowsdefender"},
	{"Privacy and Security", "Recovery", "ms-settings:recovery"},
	{"Privacy and Security", "Activation", "ms-settings:activation"},
	{"Privacy and Security", "For Developers", "ms-settings:developers"},
	{"Privacy and Security", "Location", "ms-settings:privacy-location"},
	{"Privacy and Security", "Camera", "ms-settings:privacy-webcam"},
	{"Privacy and Security", "Microphone", "ms-settings:privacy-microphone"},
	{"Privacy and Security", "Troubleshoot", "ms-settings:troubleshoot"},
}

// specialAliases maps spellings people reach for onto canonical ids. Kept small
// and deliberate: an alias is a promise to keep working, so only obvious
// synonyms earn one.
// An alias whose target is not in the catalog would report "unknown special",
// which is the truthful answer for a build that does not have that entry yet, so
// aliases are added alongside the entries they point at rather than ahead of
// them.
var specialAliases = map[string]string{
	"logoff":          "signOut",
	"logout":          "signOut",
	"signout":         "signOut",
	"reboot":          "restart",
	"poweroff":        "shutdown",
	"lockworkstation": "lock",
	"mycomputer":      "thisPC",
	"computer":        "thisPC",
	"env":             "environmentVariables",
	"environment":     "environmentVariables",
	"appsandfeatures": "installedApps",
	"programs":        "programsAndFeatures",
	"homefolder":      "userProfile",
}

// specialIndex is the case-folded lookup table, built once.
var specialIndex = func() map[string]*specialEntry {
	m := make(map[string]*specialEntry, len(specials))
	for i := range specials {
		m[strings.ToLower(specials[i].id)] = &specials[i]
	}
	return m
}()

// resolved returns what a catalog row stands for, with %VAR% already expanded.
// The args slice is copied because it is handed to an Item that normalisation
// may go on to rewrite, and the catalog is package-level state shared by every
// entry that names the same id.
func (e specialEntry) resolved() (exec string, args []string, icon string) {
	if len(e.args) > 0 {
		args = make([]string, len(e.args))
		for i, a := range e.args {
			args[i] = expandEnv(a)
		}
	}
	return expandEnv(e.target), args, expandEnv(e.icon)
}

// lookupSpecial resolves a catalog id, case-insensitively and through the alias
// table. Case folding matters because the ids are camelCase and nobody
// remembers where the capital falls.
func lookupSpecial(id string) (specialEntry, bool) {
	key := strings.ToLower(strings.TrimSpace(id))
	if key == "" {
		return specialEntry{}, false
	}
	if canonical, ok := specialAliases[key]; ok {
		key = strings.ToLower(canonical)
	}
	if e, ok := specialIndex[key]; ok {
		return *e, true
	}
	return specialEntry{}, false
}

// listSpecials prints the catalog. With this many ids the schema enum is
// otherwise the only place to discover them, and a config file is a poor place
// to go looking.
func listSpecials() int {
	group := ""
	for _, e := range specials {
		if e.group != group {
			group = e.group
			fmt.Printf("\n%s\n", strings.ToUpper(group))
		}
		target, _, _ := e.resolved()
		switch e.kind {
		case kindAction:
			target = "(built-in)"
		case kindDyn:
			target = "(submenu)"
		}
		fmt.Printf("  %-22s %-30s %s\n", e.id, e.label, target)
	}
	fmt.Printf("\n%d special(s). Use as: { \"special\": \"<id>\" }\n", len(specials))
	fmt.Printf("label and icon are supplied automatically; set either one to override.\n")
	return 0
}
