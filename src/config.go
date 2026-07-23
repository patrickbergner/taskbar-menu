package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf16"
)

// Config is the on-disk config.json.
type Config struct {
	TrayIcon *bool  `json:"trayIcon"`
	IconSize int32  `json:"iconSize"`
	Theme    string `json:"theme"`  // auto | dark | light
	Anchor   string `json:"anchor"` // taskbar | cursor
	Items    []Item `json:"items"`

	// Warnings collected while normalising: bad enum values, items that carry
	// neither exec nor children, unknown types. Surfaced in the tray tooltip
	// rather than blocking the menu.
	Warnings []string `json:"-"`
}

// Item is one menu entry. An entry with Items is a submenu and ignores Exec.
type Item struct {
	Label    string   `json:"label"`
	Type     string   `json:"type"` // item | separator
	Exec     string   `json:"exec"`
	AppID    string   `json:"appId"` // AppUserModelID of a Microsoft Store app
	Args     []string `json:"args"`
	Icon     string   `json:"icon"`
	Cwd      string   `json:"cwd"`
	Elevated bool     `json:"elevated"`
	Show     string   `json:"show"` // normal | minimized | maximized | hidden
	Tooltip  string   `json:"tooltip"`
	Items    []Item   `json:"items"`
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
		it.Icon = expandEnv(it.Icon)
		it.Cwd = expandEnv(it.Cwd)
		for j := range it.Args {
			it.Args[j] = expandEnv(it.Args[j])
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

		if it.Exec == "" && it.AppID == "" {
			c.warn("%s (%q): no exec, appId or items, skipped", where, it.Label)
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

func (it Item) showCmd() int32 {
	switch it.Show {
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
