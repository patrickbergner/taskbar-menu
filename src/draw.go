//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// palette holds the four colours the menu needs. Native HMENU popups render
// light regardless of the system theme, so every pixel inside an item is ours
// to paint.
type palette struct {
	bg       uint32
	text     uint32
	disabled uint32
	sep      uint32
	hot      uint32
}

var (
	darkPalette = palette{
		bg:       rgb(0x2C, 0x2C, 0x2C),
		text:     rgb(0xFF, 0xFF, 0xFF),
		disabled: rgb(0x71, 0x71, 0x71),
		sep:      rgb(0x45, 0x45, 0x45),
		hot:      rgb(0x3D, 0x3D, 0x3D),
	}
	lightPalette = palette{
		bg:       rgb(0xF9, 0xF9, 0xF9),
		text:     rgb(0x1A, 0x1A, 0x1A),
		disabled: rgb(0x9B, 0x9B, 0x9B),
		sep:      rgb(0xE5, 0xE5, 0xE5),
		hot:      rgb(0xE9, 0xE9, 0xE9),
	}
)

// metrics are the DPI-scaled dimensions of one menu item.
type metrics struct {
	dpi      uint32
	iconSize int32
	padX     int32
	padY     int32
	gutter   int32
	rightPad int32
	arrowW   int32
	sepH     int32
	radius   int32
	frameH   int32
}

// scale converts a 96-DPI design value into device pixels.
func scale(v int32, dpi uint32) int32 {
	if dpi == 0 || dpi == 96 {
		return v
	}
	return mulDiv(v, int32(dpi), 96)
}

func computeMetrics(dpi uint32, iconSize int32) metrics {
	return metrics{
		dpi:      dpi,
		iconSize: scale(iconSize, dpi),
		padX:     scale(10, dpi),
		padY:     scale(5, dpi),
		gutter:   scale(12, dpi),
		rightPad: scale(16, dpi),
		arrowW:   scale(14, dpi),
		sepH:     scale(7, dpi),
		radius:   scale(6, dpi),
		frameH:   scale(3, dpi),
	}
}

func resolvePalette(theme string) palette {
	switch theme {
	case "dark":
		return darkPalette
	case "light":
		return lightPalette
	default:
		if systemUsesDarkTheme() {
			return darkPalette
		}
		return lightPalette
	}
}

// systemUsesDarkTheme reads the same registry value the shell uses, so the menu
// follows Settings > Personalization > Colors.
func systemUsesDarkTheme() bool {
	var h syscall.Handle
	sub, err := syscall.UTF16PtrFromString(`SOFTWARE\Microsoft\Windows\CurrentVersion\Themes\Personalize`)
	if err != nil {
		return false
	}
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, sub, 0, syscall.KEY_READ, &h) != nil {
		return false
	}
	defer syscall.RegCloseKey(h)

	name, err := syscall.UTF16PtrFromString("AppsUseLightTheme")
	if err != nil {
		return false
	}
	var typ, data uint32
	n := uint32(unsafe.Sizeof(data))
	if syscall.RegQueryValueEx(h, name, nil, &typ, (*byte)(unsafe.Pointer(&data)), &n) != nil {
		return false
	}
	return data == 0
}

// menuFont builds the system menu font at a specific DPI. Using
// SystemParametersInfoForDpi rather than a hardcoded point size is what makes
// the text scale together with the icons.
func menuFont(dpi uint32) syscall.Handle {
	var ncm NONCLIENTMETRICSW
	ncm.Size = uint32(unsafe.Sizeof(ncm))
	if !systemParametersInfoForDpi(spiGetNonClientMetrics, &ncm, dpi) {
		return 0
	}
	return createFontIndirect(&ncm.MenuFont)
}

// ---------------------------------------------------------------------------
// WM_MEASUREITEM / WM_DRAWITEM
// ---------------------------------------------------------------------------

// measure returns the size one item occupies, in device pixels.
//
// Split out of onMeasureItem because column layout has to predict the same
// numbers before the menu exists: WM_MEASUREITEM only arrives once Windows is
// already building the popup window, far too late to decide where the columns
// break. Both callers coming through here is what stops the prediction and the
// measurement drifting apart.
//
// hdc is the caller's to own, so laying out a several-hundred-entry split does
// not pay a GetDC per entry inside WM_INITMENUPOPUP.
func (a *appState) measure(hdc syscall.Handle, n *node) (int32, int32) {
	m := a.metrics
	switch {
	case n.separator:
		return 0, m.sepH
	case n.spacer:
		return 0, m.frameH
	}

	old := selectObject(hdc, a.font)
	sz := textExtent(hdc, n.label)
	selectObject(hdc, old)

	w := m.padX + m.iconSize + m.gutter + sz.CX + m.rightPad
	if n.hasSubmenu() {
		w += m.arrowW
	}
	h := sz.CY
	if m.iconSize > h {
		h = m.iconSize
	}
	return w, h + 2*m.padY
}

func (a *appState) onMeasureItem(mis *MEASUREITEMSTRUCT) bool {
	n := a.nodeFromData(mis.ItemData)
	if n == nil {
		return false
	}
	hdc := getDC(0)
	defer releaseDC(0, hdc)

	w, h := a.measure(hdc, n)
	mis.ItemWidth = uint32(w)
	mis.ItemHeight = uint32(h)
	return true
}

func (a *appState) onDrawItem(dis *DRAWITEMSTRUCT) bool {
	if dis.CtlType != odtMenu {
		return false
	}
	n := a.nodeFromData(dis.ItemData)
	if n == nil {
		return false
	}
	m := a.metrics
	r := dis.RcItem
	hdc := dis.HDC

	bg := createSolidBrush(a.palette.bg)
	fillRect(hdc, &r, bg)
	deleteObject(bg)

	if n.spacer {
		return true
	}

	if n.separator {
		y := (r.Top + r.Bottom) / 2
		line := RECT{r.Left + m.padX, y, r.Right - m.padX, y + scale(1, m.dpi)}
		br := createSolidBrush(a.palette.sep)
		fillRect(hdc, &line, br)
		deleteObject(br)
		return true
	}

	disabled := dis.ItemState&odsDisabled != 0 || n.disabled
	if dis.ItemState&odsSelected != 0 && !disabled {
		inset := scale(3, m.dpi)
		rgn := createRoundRectRgn(r.Left+inset, r.Top, r.Right-inset, r.Bottom,
			m.radius*2, m.radius*2)
		if rgn != 0 {
			br := createSolidBrush(a.palette.hot)
			fillRgn(hdc, rgn, br)
			deleteObject(br)
			deleteObject(rgn)
		}
	}

	// Entries from a dynamic submenu defer their icon to here, so a folder of
	// three hundred files only ever pays for the twenty or so on screen.
	//
	// Both halves are deferred, and the source half is the one that matters:
	// resolving it runs SHGetFileInfoW -- which for a .lnk means opening and
	// parsing the shortcut -- so doing it eagerly would put a shell round-trip
	// per entry inside WM_INITMENUPOPUP, with the menu frozen for the duration.
	// iconFile holds the raw path until now; both lookups are cached, so
	// scrolling back over an item costs nothing.
	if n.iconLazy && n.icon == 0 && n.iconFile != "" {
		file, idx := resolveIconSource(n.iconFile, "")
		n.icon = iconFor(file, idx, m.iconSize)
		n.iconLazy = false
	}

	iconX := r.Left + m.padX
	if n.icon != 0 {
		iconY := r.Top + (r.Bottom-r.Top-m.iconSize)/2
		drawIconEx(hdc, iconX, iconY, n.icon, m.iconSize, m.iconSize)
	}

	textRight := r.Right - m.rightPad
	if n.hasSubmenu() {
		textRight -= m.arrowW
	}
	textRect := RECT{iconX + m.iconSize + m.gutter, r.Top, textRight, r.Bottom}

	colour := a.palette.text
	if disabled {
		colour = a.palette.disabled
	}
	old := selectObject(hdc, a.font)
	setBkMode(hdc, transparent)
	setTextColor(hdc, colour)
	drawText(hdc, n.label, &textRect, dtSingleLine|dtVCenter|dtLeft|dtNoPrefix|dtEndEllipsis)
	selectObject(hdc, old)

	// The flyout arrow itself is not drawn here: Windows paints it over every
	// item with a submenu regardless of MFT_OWNERDRAW, and it paints it after
	// WM_DRAWITEM returns, so anything drawn here would just be overdrawn a
	// moment later -- both on the initial paint and on every hover redraw,
	// since a hot-tracking change redraws the item without a WM_PAINT the
	// border hook could catch. arrowW above only reserves room so the label
	// doesn't run into it. Instead, a correctly-coloured replacement is posted
	// for right after this redraw cycle finishes; see wmFixArrows in main.go
	// and paintSubmenuArrows in border.go.
	if n.hasSubmenu() && borderHook != 0 {
		postMessage(a.hwnd, wmFixArrows, 0, 0)
	}
	return true
}

// paintSubmenuArrow overdraws the classic system arrow -- which is hardcoded
// to system colours that ignore the app's dark palette -- with a filled
// triangle in the right colour. r is the item's bounding rect in the same
// coordinate space as hdc (window-relative, since the caller draws through
// GetWindowDC).
func (a *appState) paintSubmenuArrow(hdc syscall.Handle, r RECT, bg, colour uint32) {
	m := a.metrics
	gutter := RECT{r.Right - m.arrowW, r.Top, r.Right, r.Bottom}
	bgBrush := createSolidBrush(bg)
	fillRect(hdc, &gutter, bgBrush)
	deleteObject(bgBrush)

	half := scale(4, m.dpi)
	cx := r.Right - m.rightPad/2 - half/2
	cy := (r.Top + r.Bottom) / 2

	pts := []POINT{
		{cx - half/2, cy - half},
		{cx + half/2, cy},
		{cx - half/2, cy + half},
	}
	rgn := createPolygonRgn(pts, polygonWinding)
	if rgn == 0 {
		return
	}
	br := createSolidBrush(colour)
	fillRgn(hdc, rgn, br)
	deleteObject(br)
	deleteObject(rgn)
}
