==============================================================================
 Taskbar Menu
==============================================================================

Launcher menu for the Windows 10/11 taskbar, driven by a single JSON file.
Pin one button to the taskbar, click it, and a popup menu of your
applications, documents, folders and URLs opens right there - a start menu
you fully control.

  - Dark mode, High DPI and Taskbar location agnostic.
  - Nested submenus, hot reload, per-entry working directory / elevation /
    window state.
  - ~10 ms per click, stays resident in the system tray.
  - No dependencies, no external tools. One static binary.
  - MIT licensed.


CONTENTS OF THIS PACKAGE
------------------------------------------------------------------------------

  TaskbarMenu.exe       the application; a single static binary
  config.example.json   one of every supported feature, a template for your
                        own config.json
  config.schema.json    JSON Schema, for editor completion and validation
  readme.txt            this file
  LICENSE               license file


GETTING STARTED
------------------------------------------------------------------------------

1. Copy this "Taskbar Menu" folder somewhere permanent, for example
   %LOCALAPPDATA%\Programs\Taskbar Menu. Keep the files together: the exe
   looks for config.json next to itself.

2. Create a config.json next to the exe. Start from config.example.json -
   copy it to config.json and edit.

3. Pin TaskbarMenu.exe to the taskbar.

4. Optional but recommended: put a shortcut to "TaskbarMenu.exe --background"
   in shell:startup, so the process is resident and the icons are already
   cached before the first click.

A minimal config.json:

    {
        "$schema": "./config.schema.json",
        "items": [
            { "label": "Budget Spreadsheet",
              "exec": "C:\\Data\\Documents\\Budget.xlsx" },
            { "type": "separator" },
            { "label": "Firefox",
              "exec": "C:\\Program Files\\Mozilla Firefox\\firefox.exe" }
        ]
    }

The config is read from config.json next to the exe, or from --config
<path>. config.schema.json ships alongside it, so an editor that honours
$schema gives completion and validation. config.example.json shows one of
every supported feature.


CONFIGURATION
------------------------------------------------------------------------------

Top level
.........

  trayIcon    (default true)
              Show a notification-area icon. The process stays resident
              either way.

  iconSize    (default 16)
              Icon edge length at 100 % scaling; scaled per monitor DPI.

  theme       (default "auto")
              auto | dark | light. auto follows the system and updates
              without a restart.

  anchor      (default "taskbar")
              taskbar sits flush against the taskbar edge; cursor opens at
              the exact pointer position.

  language    (default "auto")
              UI language for the tray menu, notifications and dialogs.
              auto detects the Windows display language; an explicit tag
              like "de" forces it, "en" forces English. See LOCALIZATION.

  items       The menu itself: an array of entries, see below.

  launchers   Optional array of pinnable launchers, see PINNED LAUNCHERS.

Entries
.......

  label       required, except for a separator and for an entry with
              special, which brings its own label. Display text.
  type        default "item". item | separator. Unknown values are skipped
              with a warning, never fatal.
  exec        an .exe, a document, a folder, a URL or a .lnk.
  appId       AppUserModelID of a Microsoft Store app. Alternative to exec,
              see MICROSOFT STORE APPS.
  special     a built-in Windows target named by a short id. Alternative
              to exec, see SPECIAL ENTRIES.
  args        default []. Arguments as separate strings; quoting is
              handled for you.
  icon        default automatic. An .ico path, or "file,index". See
              ICONS.
  cwd         working directory.
  elevated    default false. Launch with the runas verb (UAC prompt).
  show        default "normal". normal | minimized | maximized | hidden.
  confirm     default depends. Only for a power action special. Ask
              before acting. See POWER ACTIONS.
  items       turns the entry into a submenu, at any depth. exec is
              ignored when present.
  openAll     default false. Adds an "Open all" entry above a submenu's own
              entries. See NESTED SUBMENUS.
  folder      turns the entry into a submenu listing of a directory. See
              FOLDER SUBMENUS.
  depth       default 5, max 20. How many directory levels a folder
              submenu descends.
  limit       default 200, max 2000. Maximum entries shown per level of a
              folder submenu.
  sort        default "nameAsc". Sort order for a folder submenu's entries.
              See FOLDER SUBMENUS.

label, exec, icon, cwd and each args element expand %VAR% environment
references. An unset variable is left verbatim, so a typo shows up in the
menu instead of silently vanishing.


DOCUMENTS, FOLDERS AND URLS
------------------------------------------------------------------------------

exec is not limited to programs - anything with a registered default handler
works:

    { "label": "Budget",    "exec": "C:\\Data\\Documents\\Budget.xlsx" }
    { "label": "Downloads", "exec": "%USERPROFILE%\\Downloads" }
    { "label": "Emontis",   "exec": "https://emontis.io/" }

args are meaningless for these, the handler decides. If you need a specific
application rather than the registered one, name it in exec and pass the
document in args:

    { "label": "Notes",
      "exec": "C:\\Program Files\\Notepad++\\notepad++.exe",
      "args": ["C:\\Data\\notes.txt"] }


NESTED SUBMENUS
------------------------------------------------------------------------------

An entry with an items array becomes a submenu, at any depth:

    { "label": "Tools", "items": [
        { "label": "Editors", "items": [
            { "label": "Notepad", "exec": "notepad.exe" }
        ] }
    ] }

Add "openAll": true to a submenu and it leads with an "Open all" entry that launches
every entry below it in one click:

    { "label": "Morning Apps", "openAll": true, "items": [
        { "label": "Mail",    "exec": "C:\\Program Files\\Mozilla Thunderbird\\thunderbird.exe" },
        { "label": "Chat",    "exec": "C:\\Program Files\\Slack\\slack.exe" },
        { "label": "Browser", "exec": "C:\\Program Files\\Mozilla Firefox\\firefox.exe" }
    ] }

It reaches into nested submenus too, but not into a folder or special
submenu.


MICROSOFT STORE APPS
------------------------------------------------------------------------------

A Store (packaged) app has no .exe to point exec at. It is identified by an
AppUserModelID (AUMID) instead. Give that as appId and it launches through
shell:AppsFolder\<AUMID>, the same route Start uses:

    { "label": "Microsoft Photos",
      "appId": "Microsoft.Windows.Photos_8wekyb3d8bbwe!App" }

Find an app's AUMID with PowerShell. This lists the name and AppID of every
Start-menu app, Store apps included:

    Get-StartApps

appId is an alternative to exec; if both are set, appId wins.

The icon resolves automatically to the app's package logo.


ICONS
------------------------------------------------------------------------------

Omit icon and it is resolved automatically: an executable uses its own, a
document gets its file type's registered icon (.xlsx gets Excel's, .txt the
shell's), a folder gets Explorer's, a URL gets the default browser's.

To pick a specific one out of a file that holds several, append an index. A
negative index is a resource id.

    { "icon": "C:\\Windows\\System32\\imageres.dll,109" }
    { "icon": "C:\\Windows\\System32\\shell32.dll,-16" }

icon may also be an image file rather than an icon resource: .svg, .png,
.jpg, .gif, .bmp, .tiff, and .webp / .avif / .heic where the matching OS
codec is installed (the AV1 and HEIF codecs are free Store extensions).

    { "icon": "C:\\Data\\Icons\\logo.svg" }
    { "icon": "C:\\Data\\Icons\\banner.png" }

A non-square image keeps its aspect ratio: it is scaled to fit the icon box
and centered on transparency rather than squashed square. An image whose
codec is missing (e.g. an .avif on a machine without the AV1 extension)
falls back to the generic icon, exactly like a missing .dll. The menu item
is never dropped.

Two helpers make the index findable rather than guesswork:

    TaskbarMenu.exe --list-icons "C:\Windows\System32\imageres.dll"
    TaskbarMenu.exe --pick-icon  "C:\Windows\System32\imageres.dll"

--pick-icon opens the standard Windows "Change Icon" dialog and prints a
line ready to paste.

Icons are extracted at the exact pixel size the current monitor needs (16 px
at 100 %, 24 at 150 %, 32 at 200 %) and cached per size, so moving between
differently-scaled displays stays crisp.


PINNED LAUNCHERS
------------------------------------------------------------------------------

Besides the popup menu, you can pin individual launchers to the taskbar
that behave like the classic Windows launchers: each click starts a new
instance of a target, and the button itself never turns into a running-app
entry. They are defined in a separate launchers array: a flat list,
disjoint from items, since the set you pin is usually not the set you keep
in the menu:

    {
      "launchers": [
        { "id": "firefox", "label": "Firefox",
          "exec": "C:\\Program Files\\Mozilla Firefox\\firefox.exe" },
        { "id": "firefox-work", "label": "Firefox (Work)",
          "exec": "C:\\Program Files\\Mozilla Firefox\\firefox.exe",
          "args": ["-profile", "C:\\Data\\Profiles\\work"] }
      ]
    }

A launcher entry takes the same launch fields as a menu entry (exec / appId
/ special / args / icon / cwd / elevated / show) plus a required id:

  id          required. Stable key. --launch <id> resolves it and the
              generated shortcut's AppUserModelID is built from it. Also
              the .lnk file name.
  label       display text for the shortcut; also accepted by --launch as
              a fallback key.

Generate the shortcuts and pin them:

    TaskbarMenu.exe --make-launchers          write a .lnk for every
                                              launcher into .\Launchers
    TaskbarMenu.exe --make-launcher firefox   write just one
    TaskbarMenu.exe --make-launchers --out D:\Pins

Then drag a generated .lnk onto the taskbar. Each shortcut runs
"TaskbarMenu.exe --launch <id>", which starts the target and exits without a
window, so the pinned button stays a launcher instead of becoming a running
app.


SYSTEM TRAY
------------------------------------------------------------------------------

With trayIcon enabled (the default), the resident instance also shows a
notification-area icon. A left click opens the same launcher menu at the
cursor; a right click opens a small maintenance menu with Reload config,
Edit config... and Exit. The tooltip shows the config's filename, so
multiple instances (each serving a different --config) stay tellable apart.


DARK MODE
------------------------------------------------------------------------------

With theme set to auto (the default), the menu follows the system
light/dark setting and switches live, without a restart; dark and light
force one.


LOCALIZATION
------------------------------------------------------------------------------

The tray menu, the config-error and warning menu, notifications, the tray
tooltip and the power-action confirmation dialogs follow the Windows
display language automatically. This does not touch your own menu entries
(those always show whatever label you gave them) but it does cover the
special catalog's default labels (see SPECIAL ENTRIES).

Language setting:

    { "language": "auto" }   default: match the Windows display language,
                             English if unsupported
    { "language": "de" }     force German regardless of the system language
    { "language": "en" }     force English

A translation lives at lang\<tag>.json next to the exe, loaded the same
way config.json is, and hot-reloaded the same way too.

To add a language, download lang/en.json from the source repository
(https://github.com/patrickbergner/taskbar-menu/blob/main/src/lang/en.json),
save it as lang\<tag>.json next to the exe, and translate the values.
Please commit language files back to the project as a pull request.


SPECIAL ENTRIES
------------------------------------------------------------------------------

There are special entries available for most of Windows' own tools like the
device manager, task manager, Windows settings and so on:

    { "special": "deviceManager" }
    { "special": "taskManager" }
    { "special": "windowsUpdate" }
    { "special": "recycleBin" }

That is a complete entry. The label and the icon come from the catalog, so
special is the only key required. Set label or icon yourself to override
either:

    { "special": "deviceManager", "label": "Geraete-Manager" }
    { "special": "environmentVariables",
      "label": "Environment Variables (System)", "elevated": true }

special is a third way to name a target, alongside exec and appId; if more
than one is set, special wins. It works in launchers too, so a system tool
can be pinned to the taskbar:

    { "launchers": [ { "id": "devicemanager", "special": "deviceManager" } ] }

Catalog
.......

Print the catalog:

    TaskbarMenu.exe --list-specials

  Management      deviceManager diskManagement computerManagement services
                  taskScheduler eventViewer gpedit localUsers secpol
                  firewall certificates perfmon resourceMonitor taskManager
                  regedit msconfig systemInfo environmentVariables
                  diskCleanup defrag mmc
  Control Panel   controlPanelHome systemProperties networkConnections
                  programsAndFeatures displayControl mouse soundControl
                  internetOptions dateTime securityMaintenance
                  bluetoothControl userAccounts
  Settings        settings windowsUpdate installedApps displaySettings
                  soundSettings bluetoothDevices networkSettings
                  defaultApps powerSettings storage about printers
  Places          recycleBin thisPC userProfile networkFolder fonts
                  startup sendTo temp
  Folders         startMenu windowsTools desktop documents downloads
                  pictures (see FOLDER SUBMENUS)
  Shell           allApps drives controlPanel allSettings
                  (see SHELL SUBMENUS)
  Power           lock signOut sleep hibernate restart shutdown
                  (see POWER ACTIONS)

IDs are matched case-insensitively. An unknown id is skipped with a warning
rather than failing the file, so a config written for a newer build still
opens.

Two things worth knowing:

  - Not every tool exists on every edition. gpedit and secpol are absent on
    Windows Home. --config-check reports [target not found] for those; the
    entry still appears in the menu.
  - Labels follow language (see LOCALIZATION) for any id this build ships
    a translation for; an id without one, or a language not shipped at
    all, falls back to English.


FOLDER SUBMENUS
------------------------------------------------------------------------------

folder turns an entry into a submenu of a directory's contents:

    { "label": "Repos", "folder": "C:\\Data", "depth": 2 }
    { "folder": "%USERPROFILE%\\Downloads" }

The listing is read when the submenu is opened, not at startup, so it
always shows what is there.

The submenu leads with an "Open ..." item and a separator, so the folder
itself is still one click away. Directories always come before files.
Hidden and system entries are skipped, as are junctions and symlinks.

sort picks the order within each group:

  nameAsc (default)            name, A-Z
  nameDesc                     name, Z-A
  typeAsc / typeDesc           file extension, A-Z / Z-A; directories have
                               none, so they stay name-ascending
  sizeAsc / sizeDesc           file size, smallest/largest first;
                               directories stay name-ascending too - a
                               folder's total size is never computed, since
                               that would mean walking its entire subtree
                               up front
  createdAsc / createdDesc     creation time, oldest/newest first; applies
                               to directories too, by their own timestamp
  modifiedAsc / modifiedDesc   last modified time, oldest/newest first;
                               applies to directories too, by their own
                               timestamp

sort also applies to the catalog specials below that are folder submenus
(startMenu, desktop, and so on), the same as depth and limit do.

Six special entry catalog IDs are folder submenus over well-known
locations:

    { "special": "startMenu" }
    { "special": "windowsTools" }
    { "special": "desktop" }

startMenu and desktop each merge two directories (the per-user one and the
all-users one) the way Explorer presents them, so Accessories appears once
containing everything, not twice half-empty.

Two limits keep a mistake from freezing the menu, since the popup cannot
repaint while it is being filled:

  - limit (200 per level): anything beyond it becomes a clickable
    "More..." entry that opens the folder in Explorer, so the overflow is
    a door rather than a dead end.
  - A half-second deadline and a 2000-node ceiling per submenu opening.
    This is what stops a folder pointing at a disconnected network share
    from hanging the menu; you get the truncation entry instead.


SHELL SUBMENUS
------------------------------------------------------------------------------

Four submenus list things that are not directories at all, so they cannot
come from folder:

    { "special": "allApps" }       every installed application, Store
                                   apps included
    { "special": "drives" }        the drive list, with volume labels
    { "special": "controlPanel" }  the classic Control Panel applets
    { "special": "allSettings" }   the modern Settings pages, in eight
                                   groups

Enumerating the app list takes 100-300 ms, and a menu cannot repaint while
it is being filled, so the result is cached for five minutes. Reload
config from the tray clears it (which is also the answer to "I just
installed something and it is not in the list").


POWER ACTIONS
------------------------------------------------------------------------------

Six of the specials are power actions:

    { "special": "lock" }
    { "special": "signOut" }
    { "special": "sleep" }
    { "special": "hibernate" }
    { "special": "restart" }
    { "special": "shutdown" }

They require confirmation by default if they would close work:

  signOut, restart, shutdown   needs confirmation: everything you have
                               open gets closed
  lock, sleep, hibernate       acts immediately: fully reversible, nothing
                               is closed

confirm overrides either direction:

    { "special": "shutdown", "confirm": false }   no prompt, one click
    { "special": "lock",     "confirm": true }    prompt even for this

Power actions work as launchers too, so "Shut Down" can be a taskbar
button:

    { "launchers": [ { "id": "shutdown", "special": "shutdown" } ] }


COMMAND LINE
------------------------------------------------------------------------------

    TaskbarMenu.exe                        show the menu (starts the
                                           resident server on first run)
    TaskbarMenu.exe -b, --background       stay resident without showing
                                           the menu (use at logon)
    TaskbarMenu.exe -c, --config <path>    use a different config file
    TaskbarMenu.exe --config-check         validate the config, print the
                                           menu tree, exit
    TaskbarMenu.exe --config-format        pretty-print the config file in
                                           place, exit
    TaskbarMenu.exe --list-icons <file>    how many icons a file holds
    TaskbarMenu.exe --pick-icon  <file>    open the Windows icon picker
    TaskbarMenu.exe --list-specials        list the built-in "special"
                                           entry ids
    TaskbarMenu.exe --launch <id>          start one launcher entry (what a
                                           pinned shortcut runs)
    TaskbarMenu.exe --make-launcher <id> [--out <dir>]
                                           write one pinnable .lnk
    TaskbarMenu.exe --make-launchers     [--out <dir>]
                                           write a .lnk for every launcher
    TaskbarMenu.exe -h, --help
    TaskbarMenu.exe -v, --version


TROUBLESHOOTING
------------------------------------------------------------------------------

A config error never produces an empty menu: the parse error, with its line
and column, becomes a clickable menu item that opens the file. Run
"TaskbarMenu.exe --config-check" to see the same information on the console.

Edits to config.json are picked up automatically. The file is re-read
whenever it has changed, so no restart is needed.

Set TASKBARMENU_LOG=1 to log startup and reloads to %TEMP%\taskbarmenu.log,
or set it to a path of your own.


MORE
------------------------------------------------------------------------------

Screenshots, the full README and the source:

    https://github.com/patrickbergner/taskbar-menu


LICENSE
------------------------------------------------------------------------------

MIT. See the LICENSE file in this folder.
