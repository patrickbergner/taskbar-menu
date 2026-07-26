package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// Config is the on-disk config.json.
type Config struct {
	TrayIcon  *bool      `json:"trayIcon"`
	IconSize  int32      `json:"iconSize"`
	Theme     string     `json:"theme"`  // auto | dark | light
	Anchor    string     `json:"anchor"` // taskbar | cursor
	Items     []Item     `json:"items"`
	Launchers []Launcher `json:"launchers"`

	// HasDynamic is set during normalisation when any entry enumerates itself
	// when opened. It is what lets a config without such entries keep the
	// build-once-and-cache fast path untouched.
	HasDynamic bool `json:"-"`

	// Warnings collected while normalising: bad enum values, items that carry
	// neither exec nor children, unknown types. Surfaced in the tray tooltip
	// rather than blocking the menu.
	Warnings []string `json:"-"`
}

// Launcher is a standalone, pin-to-taskbar entry. It is deliberately separate
// from the menu Items: a launcher is a flat target (no submenus or separators)
// that TaskbarMenu can run directly (--launch <id>) or turn into a pinnable
// shortcut (--make-launcher). The id is the stable key both the command line and
// the generated shortcut's AppUserModelID are built from, so it must not change
// once a launcher has been pinned.
type Launcher struct {
	Id       string   `json:"id"`
	Label    string   `json:"label"`
	Exec     string   `json:"exec"`
	AppID    string   `json:"appId"`   // AppUserModelID of a Microsoft Store app
	Special  string   `json:"special"` // catalog id; see special.go
	Args     []string `json:"args"`
	Icon     string   `json:"icon"`
	Cwd      string   `json:"cwd"`
	Elevated bool     `json:"elevated"`
	Show     string   `json:"show"`    // normal | minimized | maximized | hidden
	Confirm  *bool    `json:"confirm"` // power actions only; see Item.Confirm
}

// Item is one menu entry. An entry with Items is a submenu and ignores Exec.
type Item struct {
	Label    string   `json:"label"`
	Type     string   `json:"type"` // item | separator
	Exec     string   `json:"exec"`
	AppID    string   `json:"appId"`   // AppUserModelID of a Microsoft Store app
	Special  string   `json:"special"` // catalog id; see special.go
	Folder   string   `json:"folder"`  // directory enumerated live into a submenu
	Depth    int      `json:"depth"`   // folder recursion, 1..20
	Limit    int      `json:"limit"`   // max entries per level
	Args     []string `json:"args"`
	Icon     string   `json:"icon"`
	Cwd      string   `json:"cwd"`
	Elevated bool     `json:"elevated"`
	Show     string   `json:"show"` // normal | minimized | maximized | hidden
	Tooltip  string   `json:"tooltip"`
	Items    []Item   `json:"items"`

	// Confirm gates a power action behind a yes/no dialog. A *bool for the same
	// reason TrayIcon is one: the default is true for the destructive actions
	// and false for the reversible ones, so "not set" has to be distinguishable
	// from an explicit false.
	Confirm *bool `json:"confirm"`
}

// Show state constants, mirroring the SW_* values so they can be handed
// straight to ShellExecuteEx.
const (
	showNormal    = 1
	showMinimized = 2
	showMaximized = 3
	showHidden    = 0
)

// stamp is the cheap change detector used for hot reload. Comparing mtime and
// size on every show costs ~50us, needs no watcher goroutine, and cannot miss
// an edit the way a debounced notification can.
type stamp struct {
	mod  time.Time
	size int64
	ok   bool
}

func statStamp(path string) stamp {
	fi, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{mod: fi.ModTime(), size: fi.Size(), ok: true}
}

func (s stamp) equal(o stamp) bool {
	return s.ok == o.ok && s.size == o.size && s.mod.Equal(o.mod)
}

func defaultConfig() *Config {
	t := true
	return &Config{
		TrayIcon: &t,
		IconSize: 16,
		Theme:    "auto",
		Anchor:   "taskbar",
	}
}

// LoadConfig reads and normalises config.json. A parse error is returned to the
// caller, which renders it as a single clickable menu item rather than failing
// silently -- with hot reload that makes fixing the file self-correcting.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = decodeBOM(raw)

	cfg := defaultConfig()
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", jsonErrorPosition(raw, err), err)
	}
	cfg.normalize()
	return cfg, nil
}

// decodeBOM normalises whatever a Windows text editor produced into plain
// UTF-8.
//
// This is not hypothetical tidiness: Notepad and PowerShell's Set-Content both
// write a UTF-8 BOM by default, and encoding/json rejects one with
// "invalid character 'ï' looking for beginning of value" -- a message that
// gives no hint that the file is fine and the encoding is not.
func decodeBOM(raw []byte) []byte {
	switch {
	case len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF:
		return raw[3:]
	case len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE:
		return utf16ToUTF8(raw[2:], binary.LittleEndian)
	case len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF:
		return utf16ToUTF8(raw[2:], binary.BigEndian)
	}
	return raw
}

func utf16ToUTF8(b []byte, order binary.ByteOrder) []byte {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, order.Uint16(b[i:]))
	}
	return []byte(string(utf16.Decode(units)))
}

// jsonErrorPosition turns a byte offset into a line:col prefix, since the raw
// offset in encoding/json's message is useless when staring at a text editor.
func jsonErrorPosition(raw []byte, err error) string {
	var offset int64 = -1
	switch e := err.(type) {
	case *json.SyntaxError:
		offset = e.Offset
	case *json.UnmarshalTypeError:
		offset = e.Offset
	}
	if offset < 0 || offset > int64(len(raw)) {
		return "config"
	}
	line, col := 1, 1
	for _, b := range raw[:offset] {
		if b == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return fmt.Sprintf("line %d col %d", line, col)
}

func (c *Config) normalize() {
	if c.IconSize <= 0 {
		c.IconSize = 16
	}
	if c.IconSize > 64 {
		c.IconSize = 64
	}
	c.Theme = normEnum(c, "theme", c.Theme, "auto", "auto", "dark", "light")
	c.Anchor = normEnum(c, "anchor", c.Anchor, "taskbar", "taskbar", "cursor")
	if c.TrayIcon == nil {
		t := true
		c.TrayIcon = &t
	}
	c.Items = c.normItems(c.Items, "")
	c.normLaunchers()
}

// normLaunchers validates the flat launchers list the same way normItems handles
// a menu level, minus submenus and separators. An entry with no id is dropped
// with a warning: the id is the key --launch resolves and the shortcut's
// AppUserModelID is derived from, so a launcher without one is unusable.
func (c *Config) normLaunchers() {
	out := make([]Launcher, 0, len(c.Launchers))
	for i := range c.Launchers {
		l := c.Launchers[i]
		where := fmt.Sprintf("launchers[%d]", i)

		l.Id = strings.TrimSpace(l.Id)
		l.Label = expandEnv(l.Label)
		l.Exec = expandEnv(l.Exec)
		l.AppID = strings.TrimSpace(l.AppID)
		l.Special = strings.TrimSpace(l.Special)
		l.Icon = expandEnv(l.Icon)
		l.Cwd = expandEnv(l.Cwd)
		for j := range l.Args {
			l.Args[j] = expandEnv(l.Args[j])
		}

		if l.Id == "" {
			c.warn("%s: missing id, skipped", where)
			continue
		}

		// A launcher resolves a special exactly as a menu item does; the id stays
		// the launcher's own, since that is what --launch and the pinned
		// shortcut's AppUserModelID are keyed on.
		if l.Special != "" {
			e, ok := lookupSpecial(l.Special)
			if !ok {
				c.warn("%s (%q): unknown special %q, skipped", where, l.Id, l.Special)
				continue
			}
			if l.Exec != "" || l.AppID != "" {
				c.warn("%s (%q): both special and exec/appId given, using special", where, l.Id)
				l.Exec, l.AppID = "", ""
			}
			if l.Label == "" {
				l.Label = e.label
			}
			exec, args, icon := e.resolved()
			if l.Icon == "" {
				l.Icon = icon
			}
			switch e.kind {
			case kindExec:
				l.Exec = exec
				if len(l.Args) == 0 {
					l.Args = args
				}
				l.Special = ""
			case kindAction:
				l.Special = e.id
				if l.Confirm == nil {
					d := confirmDefault(e.id)
					l.Confirm = &d
				}
			case kindDyn:
				// A launcher is a flat target by definition; there is nowhere for
				// a submenu to open from a taskbar button.
				c.warn("%s (%q): special %q is a submenu and cannot be a launcher, skipped",
					where, l.Id, e.id)
				continue
			default:
				c.warn("%s (%q): special %q is not available in this build, skipped",
					where, l.Id, e.id)
				continue
			}
		}

		// exec and appId are two ways to name the same target; appId wins, exactly
		// as it does for a menu Item.
		if l.AppID != "" && l.Exec != "" {
			c.warn("%s (%q): both exec and appId given, using appId", where, l.Id)
			l.Exec = ""
		}
		if l.Exec == "" && l.AppID == "" && l.Special == "" {
			c.warn("%s (%q): no exec, appId or special, skipped", where, l.Id)
			continue
		}

		switch strings.ToLower(l.Show) {
		case "", "normal":
			l.Show = "normal"
		case "minimized", "maximized", "hidden":
			l.Show = strings.ToLower(l.Show)
		default:
			c.warn("%s (%q): unknown show %q, using \"normal\"", where, l.Id, l.Show)
			l.Show = "normal"
		}

		out = append(out, l)
	}
	c.Launchers = out
}

// findLauncher resolves the key --launch / --make-launcher was given: id first,
// then label as a fallback, so a stable id is preferred but a unique label still
// works.
func findLauncher(c *Config, key string) (Launcher, bool) {
	for _, l := range c.Launchers {
		if l.Id == key {
			return l, true
		}
	}
	for _, l := range c.Launchers {
		if l.Label == key {
			return l, true
		}
	}
	return Launcher{}, false
}

func normEnum(c *Config, field, v, def string, allowed ...string) string {
	if v == "" {
		return def
	}
	lv := strings.ToLower(v)
	for _, a := range allowed {
		if lv == a {
			return lv
		}
	}
	c.warn("%s: unknown value %q, using %q", field, v, def)
	return def
}

func (c *Config) warn(format string, args ...any) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(format, args...))
}

// normItems validates one level of the tree and recurses. Unknown "type"
// values are dropped with a warning rather than failing the whole file -- that
// is what keeps the schema forward-compatible with menuApp's special codes.
func (c *Config) normItems(items []Item, path string) []Item {
	out := make([]Item, 0, len(items))
	for i := range items {
		it := items[i]
		where := fmt.Sprintf("%s[%d]", path, i)

		it.Type = strings.ToLower(strings.TrimSpace(it.Type))
		switch it.Type {
		case "", "item":
			it.Type = "item"
		case "separator":
			out = append(out, it)
			continue
		default:
			c.warn("%s: unknown type %q, skipped", where, it.Type)
			continue
		}

		it.Label = expandEnv(it.Label)
		it.Exec = expandEnv(it.Exec)
		it.AppID = strings.TrimSpace(it.AppID)
		it.Special = strings.TrimSpace(it.Special)
		it.Folder = expandEnv(it.Folder)
		it.Icon = expandEnv(it.Icon)
		it.Cwd = expandEnv(it.Cwd)
		for j := range it.Args {
			it.Args[j] = expandEnv(it.Args[j])
		}

		// A special is resolved before the label check, because supplying the
		// label is half of what the catalog is for -- {"special": "taskManager"}
		// with no label at all is the shortest useful entry there is.
		if it.Special != "" && len(it.Items) > 0 {
			// Named without a label here on purpose: the catalog label is not
			// applied when the special is discarded, so there is nothing to quote.
			c.warn("%s: items given with special %q, ignoring special", where, it.Special)
			it.Special = ""
		}
		if it.Special != "" {
			e, ok := lookupSpecial(it.Special)
			if !ok {
				// Same forward-compatibility policy as an unknown type: a config
				// written for a later build still opens, minus the entry it names.
				c.warn("%s: unknown special %q, skipped", where, it.Special)
				continue
			}
			if it.Label == "" {
				it.Label = e.label
			}
			exec, args, icon := e.resolved()
			if it.Icon == "" {
				it.Icon = icon
			}
			if it.Exec != "" || it.AppID != "" {
				c.warn("%s (%q): both special and exec/appId given, using special", where, it.Label)
				it.Exec, it.AppID = "", ""
			}
			switch e.kind {
			case kindExec:
				it.Exec = exec
				if len(it.Args) == 0 {
					it.Args = args
				}
				// Fully desugared: nothing downstream needs to know this entry
				// began life as a special, which is why the catalog costs
				// buildNodes, launch and --check no changes at all.
				it.Special = ""
			case kindAction:
				// Survives normalisation as a live special: there is no exec to
				// desugar into, so buildNodes attaches the built-in instead.
				it.Special = e.id
				if it.Confirm == nil {
					d := confirmDefault(e.id)
					it.Confirm = &d
				}
			case kindDyn:
				it.Special = e.id
				c.HasDynamic = true
			default:
				c.warn("%s (%q): special %q is not available in this build, skipped",
					where, it.Label, e.id)
				continue
			}
		}

		// folder turns the entry into a submenu enumerated when it is opened.
		// items wins over it for the same reason it wins over special: an
		// explicit list is never something to second-guess.
		if it.Folder != "" && len(it.Items) > 0 {
			c.warn("%s: items given with folder, ignoring folder", where)
			it.Folder = ""
		}
		if it.Folder != "" && it.Special != "" {
			c.warn("%s: special given with folder, ignoring folder", where)
			it.Folder = ""
		}
		if it.Folder != "" {
			if it.Label == "" {
				it.Label = filepath.Base(strings.TrimRight(it.Folder, `\/`))
			}
			c.HasDynamic = true
		}

		// This file warns about every key that will be ignored, so these have to
		// as well -- and the test is what the key actually applies to, not
		// merely that some special is present. depth and limit belong to a
		// directory listing, confirm to a power action; asking for either on the
		// wrong kind of entry is a silent no-op otherwise.
		//
		// The values themselves are deliberately left unclamped here: the clamp
		// lives in dynSourceFor, which is the only thing that reads them, and
		// having it in one place means a changed default cannot disagree with
		// itself. A plain exec entry keeps depth 0 rather than a meaningless 1.
		if it.Depth != 0 || it.Limit != 0 {
			if !it.listsADirectory() {
				c.warn("%s (%q): depth/limit only apply to a folder submenu", where, it.Label)
			}
			if it.Depth > maxDepth {
				c.warn("%s (%q): depth %d exceeds the maximum of %d", where, it.Label, it.Depth, maxDepth)
			}
			if it.Limit > maxLimit {
				c.warn("%s (%q): limit %d exceeds the maximum of %d", where, it.Label, it.Limit, maxLimit)
			}
		}

		if it.Confirm != nil && !it.isPowerAction() {
			c.warn("%s (%q): confirm only applies to a power action", where, it.Label)
		}

		if it.Label == "" {
			c.warn("%s: missing label, skipped", where)
			continue
		}

		if len(it.Items) > 0 {
			it.Items = c.normItems(it.Items, where+".items")
			if len(it.Items) == 0 {
				c.warn("%s (%q): submenu has no usable entries, skipped", where, it.Label)
				continue
			}
			out = append(out, it)
			continue
		}

		// appId names a Microsoft Store app by its AppUserModelID; it is turned
		// into a shell:AppsFolder target at launch. exec and appId are two ways
		// to say the same thing, so if both are given appId wins, matching how
		// items overrides exec.
		if it.AppID != "" && it.Exec != "" {
			c.warn("%s (%q): both exec and appId given, using appId", where, it.Label)
			it.Exec = ""
		}

		// A surviving Special is a built-in or a dynamic submenu, and a folder is
		// a submenu; none of them has an exec by nature.
		if it.Exec == "" && it.AppID == "" && it.Special == "" && it.Folder == "" {
			c.warn("%s (%q): no exec, appId, special, folder or items, skipped", where, it.Label)
			continue
		}

		switch strings.ToLower(it.Show) {
		case "", "normal":
			it.Show = "normal"
		case "minimized", "maximized", "hidden":
			it.Show = strings.ToLower(it.Show)
		default:
			c.warn("%s (%q): unknown show %q, using \"normal\"", where, it.Label, it.Show)
			it.Show = "normal"
		}

		out = append(out, it)
	}
	return out
}

func (it Item) showCmd() int32    { return showCmdOf(it.Show) }
func (l Launcher) showCmd() int32 { return showCmdOf(l.Show) }

// confirms reports whether this entry asks before acting. Normalisation always
// fills Confirm in for a power special, so nil here means "not a power entry".
func (it Item) confirms() bool    { return it.Confirm != nil && *it.Confirm }
func (l Launcher) confirms() bool { return l.Confirm != nil && *l.Confirm }

// isPowerAction and listsADirectory answer "does this key apply here", which is
// what the ignored-key warnings need. Both are asked during normalisation, at
// which point a kindExec special has already been desugared away and a
// surviving Special names either an action or a submenu.
func (it Item) isPowerAction() bool {
	_, ok := powerOpFor(it.Special)
	return ok
}

func (it Item) listsADirectory() bool {
	if it.Folder != "" {
		return true
	}
	e, ok := lookupSpecial(it.Special)
	return ok && e.kind == kindDyn && e.dynKind == dynFolder
}

func showCmdOf(show string) int32 {
	switch show {
	case "minimized":
		return showMinimized
	case "maximized":
		return showMaximized
	case "hidden":
		return showHidden
	default:
		return showNormal
	}
}

// expandEnv expands Windows-style %VAR% references. Unset variables are left
// verbatim so a typo is visible in the menu instead of silently vanishing.
func expandEnv(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '%' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i+1:], '%')
		if end < 0 {
			b.WriteString(s[i:])
			break
		}
		name := s[i+1 : i+1+end]
		if name == "" { // "%%" -> literal percent
			b.WriteByte('%')
			i += 2
			continue
		}
		if v, ok := os.LookupEnv(name); ok {
			b.WriteString(v)
		} else {
			b.WriteString(s[i : i+end+2])
		}
		i += end + 2
	}
	return b.String()
}
