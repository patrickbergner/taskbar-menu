==============================================================================
 Taskbar Menu
==============================================================================

Launcher menu for the Windows 10/11 taskbar, driven by a single JSON file.
Pin one button to the taskbar, click it, and a popup menu of your
applications, documents, folders and URLs opens right there - a start menu
you fully control.

  - Dark mode, High DPI and taskbar location agnostic.
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

Run "TaskbarMenu.exe --check" to validate the config and print the resulting
menu tree without opening anything.


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

  items       The menu itself: an array of entries, see below.

  launchers   Optional array of pinnable launchers, see PINNED LAUNCHERS.

Entries
.......

  label       required. Display text.
  type        default "item". item | separator. Unknown values are skipped
              with a warning, never fatal.
  exec        an .exe, a document, a folder, a URL or a .lnk.
  appId       AppUserModelID of a Microsoft Store app. Alternative to exec,
              see MICROSOFT STORE APPS.
  args        default []. Arguments as separate strings; quoting is handled
              for you.
  icon        default automatic. An .ico path, or "file,index". See ICONS.
  cwd         working directory.
  elevated    default false. Launch with the runas verb (UAC prompt).
  show        default "normal". normal | minimized | maximized | hidden.
  items       turns the entry into a submenu, at any depth. exec is ignored
              when present.

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

appId is an alternative to exec; if both are set, appId wins. The icon
resolves automatically to the app's package logo.


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

Besides the popup menu, you can pin individual launchers to the taskbar that
behave like the classic Windows launchers: each click starts a new instance
of a target, and the button itself never turns into a running-app entry.

They are defined in a separate launchers array: a flat list, disjoint from
items, since the set you pin is usually not the set you keep in the menu:

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
/ args / icon / cwd / elevated / show) plus a required id:

  id          required. Stable key. --launch <id> resolves it and the
              generated shortcut's AppUserModelID is built from it. Also the
              .lnk file name.
  label       display text for the shortcut; also accepted by --launch as a
              fallback key.

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

With theme set to auto (the default), the menu follows the system light/dark
setting and switches live, without a restart; dark and light force one.


COMMAND LINE
------------------------------------------------------------------------------

    TaskbarMenu.exe                        show the menu (starts the
                                           resident server on first run)
    TaskbarMenu.exe -b, --background       stay resident without showing
                                           the menu (use at logon)
    TaskbarMenu.exe -c, --config <path>    use a different config file
    TaskbarMenu.exe --check                validate the config, print the
                                           menu tree, exit
    TaskbarMenu.exe --list-icons <file>    how many icons a file holds
    TaskbarMenu.exe --pick-icon  <file>    open the Windows icon picker
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
"TaskbarMenu.exe --check" to see the same information on the console.

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
