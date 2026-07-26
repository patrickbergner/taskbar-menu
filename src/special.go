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
	{id: "deviceManager", group: "Management",
		// -201, not index 0: index 0 of devmgr.dll is the driver-signing badge.
		// This is the id the shell class registration itself points at.
		target: sys32 + "devmgmt.msc", icon: sys32 + "devmgr.dll,-201"},
	{id: "diskManagement", group: "Management",
		target: sys32 + "diskmgmt.msc", icon: sys32 + "dmdskres.dll,0"},
	{id: "computerManagement", group: "Management",
		target: sys32 + "compmgmt.msc", icon: sys32 + "Mycomput.dll,2"},
	{id: "services", group: "Management",
		target: sys32 + "services.msc", icon: sys32 + "filemgmt.dll,0"},
	{id: "taskScheduler", group: "Management",
		target: sys32 + "taskschd.msc", icon: sys32 + "miguiresource.dll,1"},
	{id: "eventViewer", group: "Management",
		target: sys32 + "eventvwr.msc", icon: sys32 + "miguiresource.dll,0"},
	{id: "gpedit", group: "Management",
		target: sys32 + "gpedit.msc", icon: sys32 + "gpedit.dll,0"},
	// filemgmt.dll holds no users icon -- index 0 there is the Services gears,
	// which is what this entry drew before anyone looked at it.
	{id: "localUsers", group: "Management",
		target: sys32 + "lusrmgr.msc", icon: sys32 + "netplwiz.exe,0"},
	{id: "secpol", group: "Management",
		target: sys32 + "secpol.msc", icon: sys32 + "wsecedit.dll,0"},
	{id: "firewall", group: "Management",
		target: sys32 + "WF.msc", icon: sys32 + "AuthFWGP.dll,-101"},
	{id: "certificates", group: "Management",
		target: sys32 + "certmgr.msc", icon: sys32 + "certmgr.dll,0"},
	{id: "perfmon", group: "Management",
		target: sys32 + "perfmon.msc", icon: sys32 + "wdc.dll,-108"},
	{id: "resourceMonitor", group: "Management",
		target: sys32 + "perfmon.exe", args: []string{"/res"}, icon: sys32 + "wdc.dll,-108"},
	{id: "taskManager", group: "Management",
		target: sys32 + "taskmgr.exe"},
	{id: "regedit", group: "Management",
		target: `%SystemRoot%\regedit.exe`, icon: `%SystemRoot%\regedit.exe,-100`},
	{id: "msconfig", group: "Management",
		target: sys32 + "msconfig.exe", icon: sys32 + "msconfig.exe,-3000"},
	{id: "systemInfo", group: "Management",
		target: sys32 + "msinfo32.exe"},
	// rundll32 holds no icon resources of its own, so this one genuinely needs a
	// spec: the dialog belongs to System Properties, hence sysdm.cpl.
	{id: "environmentVariables", group: "Management",
		target: sys32 + "rundll32.exe", args: []string{"sysdm.cpl,EditEnvironmentVariables"},
		icon: sys32 + "sysdm.cpl,0"},
	{id: "diskCleanup", group: "Management",
		target: sys32 + "cleanmgr.exe"},
	{id: "defrag", group: "Management",
		target: sys32 + "dfrgui.exe"},
	{id: "mmc", group: "Management",
		target: sys32 + "mmc.exe"},

	// --- Control Panel ----------------------------------------------------
	// Every .cpl is its own icon container, so none of these carry a spec.
	{id: "controlPanelHome", group: "Control Panel",
		target: sys32 + "control.exe"},
	{id: "systemProperties", group: "Control Panel",
		target: sys32 + "sysdm.cpl"},
	{id: "networkConnections", group: "Control Panel",
		target: sys32 + "ncpa.cpl"},
	{id: "programsAndFeatures", group: "Control Panel",
		target: sys32 + "appwiz.cpl"},
	{id: "displayControl", group: "Control Panel",
		target: sys32 + "desk.cpl"},
	{id: "mouse", group: "Control Panel",
		target: sys32 + "main.cpl"},
	{id: "soundControl", group: "Control Panel",
		target: sys32 + "mmsys.cpl"},
	{id: "internetOptions", group: "Control Panel",
		target: sys32 + "inetcpl.cpl"},
	{id: "dateTime", group: "Control Panel",
		target: sys32 + "timedate.cpl"},
	{id: "securityMaintenance", group: "Control Panel",
		target: sys32 + "wscui.cpl"},
	// The one .cpl here that needs a spec: index 0 of bthprops.cpl is a warning
	// triangle. -200 of DevicePairingFolder.dll is what the Bluetooth Devices
	// shell class registers.
	{id: "bluetoothControl", group: "Control Panel",
		target: sys32 + "bthprops.cpl", icon: sys32 + "DevicePairingFolder.dll,-200"},
	{id: "userAccounts", group: "Control Panel",
		target: sys32 + "netplwiz.exe"},

	// --- Settings ---------------------------------------------------------
	// One shared icon for every page, deliberately. The association for
	// ms-settings: resolves to a package resource path the icon extractor
	// cannot read, and resource -10 of SystemSettings.exe is what Windows' own
	// Win+X shortcuts point at. Per-page icons can be added later without a
	// schema change.
	{id: "settings", group: "Settings",
		target: "ms-settings:", icon: settingsIcon},
	{id: "windowsUpdate", group: "Settings",
		target: "ms-settings:windowsupdate", icon: settingsIcon},
	{id: "installedApps", group: "Settings",
		target: "ms-settings:appsfeatures", icon: settingsIcon},
	{id: "displaySettings", group: "Settings",
		target: "ms-settings:display", icon: settingsIcon},
	{id: "soundSettings", group: "Settings",
		target: "ms-settings:sound", icon: settingsIcon},
	{id: "bluetoothDevices", group: "Settings",
		target: "ms-settings:bluetooth", icon: settingsIcon},
	{id: "networkSettings", group: "Settings",
		target: "ms-settings:network", icon: settingsIcon},
	{id: "defaultApps", group: "Settings",
		target: "ms-settings:defaultapps", icon: settingsIcon},
	{id: "powerSettings", group: "Settings",
		target: "ms-settings:powersleep", icon: settingsIcon},
	{id: "storage", group: "Settings",
		target: "ms-settings:storagesense", icon: settingsIcon},
	{id: "about", group: "Settings",
		target: "ms-settings:about", icon: settingsIcon},
	{id: "printers", group: "Settings",
		target: "ms-settings:printers", icon: settingsIcon},

	// --- Places -----------------------------------------------------------
	// Shell monikers carry no extractable resource, so their icon comes from
	// the shell image factory instead; see isShellMoniker in icons.go. That is
	// not a workaround but the better answer: it gives the Recycle Bin its
	// current full-or-empty icon rather than a fixed frame.
	{id: "recycleBin", group: "Places",
		target: "shell:RecycleBinFolder"},
	{id: "thisPC", group: "Places",
		target: "shell:MyComputerFolder"},
	{id: "userProfile", group: "Places",
		target: "%USERPROFILE%"},
	{id: "networkFolder", group: "Places",
		target: "shell:NetworkPlacesFolder"},
	{id: "fonts", group: "Places",
		target: "shell:Fonts"},
	{id: "startup", group: "Places",
		target: "shell:Startup"},
	{id: "sendTo", group: "Places",
		target: "shell:SendTo"},
	{id: "temp", group: "Places",
		target: "%TEMP%"},

	// --- Folders (dynamic submenus) ----------------------------------------
	// Enumerated when opened; see dynamic.go. Icons come from the folder itself.
	{id: "startMenu", group: "Folders", kind: kindDyn, roots: []string{
		`%APPDATA%\Microsoft\Windows\Start Menu\Programs`,
		`%ProgramData%\Microsoft\Windows\Start Menu\Programs`,
	}},
	{id: "windowsTools", group: "Folders", kind: kindDyn, roots: []string{
		`%ProgramData%\Microsoft\Windows\Start Menu\Programs\Administrative Tools`,
	}},
	{id: "desktop", group: "Folders", kind: kindDyn, roots: []string{
		`%USERPROFILE%\Desktop`,
		`%PUBLIC%\Desktop`,
	}},
	{id: "documents", group: "Folders", kind: kindDyn,
		roots: []string{`%USERPROFILE%\Documents`}},
	{id: "downloads", group: "Folders", kind: kindDyn,
		roots: []string{`%USERPROFILE%\Downloads`}},
	{id: "pictures", group: "Folders", kind: kindDyn,
		roots: []string{`%USERPROFILE%\Pictures`}},

	// --- Shell namespace (dynamic submenus) --------------------------------
	// Listed through IEnumShellItems rather than the filesystem, because none of
	// these are directories: a Store app has no path at all, and a Control Panel
	// applet is a namespace item.
	{id: "allApps", group: "Shell", kind: kindDyn,
		dynKind: dynShell, roots: []string{"shell:AppsFolder"},
		icon: sys32 + "shell32.dll,-16"},
	{id: "drives", group: "Shell", kind: kindDyn,
		dynKind: dynShell, roots: []string{"shell:MyComputerFolder"}, drivesOnly: true,
		icon: sys32 + "imageres.dll,-30"},
	{id: "controlPanel", group: "Shell", kind: kindDyn,
		dynKind: dynShell, roots: []string{"shell:ControlPanelFolder"},
		icon: sys32 + "control.exe,0"},
	{id: "allSettings", group: "Shell", kind: kindDyn,
		dynKind: dynSettings, icon: settingsIcon},

	// --- Power -------------------------------------------------------------
	// These are the only entries that are not a target at all; see power.go.
	// The icons come from shell32, which is where the classic Shut Down dialog
	// has always kept them.
	{id: "lock", group: "Power", kind: kindAction,
		icon: sys32 + "shell32.dll,47"},
	{id: "signOut", group: "Power", kind: kindAction,
		icon: sys32 + "imageres.dll,209"},
	{id: "sleep", group: "Power", kind: kindAction,
		icon: sys32 + "shell32.dll,25"},
	// Hibernate deliberately shares Sleep's glyph: Windows ships no distinct
	// hibernate icon, and inventing a different one would imply a distinction
	// the system itself does not draw.
	{id: "hibernate", group: "Power", kind: kindAction,
		icon: sys32 + "shell32.dll,25"},
	{id: "restart", group: "Power", kind: kindAction,
		icon: sys32 + "imageres.dll,229"},
	{id: "shutdown", group: "Power", kind: kindAction,
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
// group is a stable identifier, like specialEntry.id -- not display text.
// settingsGroupLabel resolves it to the group header shown in the menu.
type settingsPage struct{ group, uri string }

var settingsPages = []settingsPage{
	{"system", "ms-settings:display"},
	{"system", "ms-settings:sound"},
	{"system", "ms-settings:notifications"},
	{"system", "ms-settings:powersleep"},
	{"system", "ms-settings:storagesense"},
	{"system", "ms-settings:multitasking"},
	{"system", "ms-settings:clipboard"},
	{"system", "ms-settings:about"},

	{"devices", "ms-settings:bluetooth"},
	{"devices", "ms-settings:printers"},
	{"devices", "ms-settings:mousetouchpad"},
	{"devices", "ms-settings:devices-touchpad"},
	{"devices", "ms-settings:autoplay"},
	{"devices", "ms-settings:usb"},

	{"network", "ms-settings:network-status"},
	{"network", "ms-settings:network-wifi"},
	{"network", "ms-settings:network-ethernet"},
	{"network", "ms-settings:network-vpn"},
	{"network", "ms-settings:network-mobilehotspot"},
	{"network", "ms-settings:network-proxy"},

	{"personalisation", "ms-settings:personalization-background"},
	{"personalisation", "ms-settings:personalization-colors"},
	{"personalisation", "ms-settings:themes"},
	{"personalisation", "ms-settings:lockscreen"},
	{"personalisation", "ms-settings:taskbar"},
	{"personalisation", "ms-settings:personalization-start"},
	{"personalisation", "ms-settings:fonts"},

	{"apps", "ms-settings:appsfeatures"},
	{"apps", "ms-settings:defaultapps"},
	{"apps", "ms-settings:startupapps"},
	{"apps", "ms-settings:optionalfeatures"},

	{"accounts", "ms-settings:yourinfo"},
	{"accounts", "ms-settings:signinoptions"},
	{"accounts", "ms-settings:otherusers"},
	{"accounts", "ms-settings:backup"},

	{"timeAndLanguage", "ms-settings:dateandtime"},
	{"timeAndLanguage", "ms-settings:regionlanguage"},
	{"timeAndLanguage", "ms-settings:typing"},

	{"privacyAndSecurity", "ms-settings:windowsupdate"},
	{"privacyAndSecurity", "ms-settings:windowsdefender"},
	{"privacyAndSecurity", "ms-settings:recovery"},
	{"privacyAndSecurity", "ms-settings:activation"},
	{"privacyAndSecurity", "ms-settings:developers"},
	{"privacyAndSecurity", "ms-settings:privacy-location"},
	{"privacyAndSecurity", "ms-settings:privacy-webcam"},
	{"privacyAndSecurity", "ms-settings:privacy-microphone"},
	{"privacyAndSecurity", "ms-settings:troubleshoot"},
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
	// englishDefaults(), not the package-level ui: this listing is a stable
	// reference for the id regardless of the active language, so it always
	// re-reads the embedded English catalog directly rather than whatever
	// language happens to be loaded.
	en := englishDefaults()
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
		fmt.Printf("  %-22s %-30s %s\n", e.id, en.Specials[e.id], target)
	}
	fmt.Printf("\n%d special(s). Use as: { \"special\": \"<id>\" }\n", len(specials))
	fmt.Printf("label and icon are supplied automatically; set either one to override.\n")
	return 0
}
