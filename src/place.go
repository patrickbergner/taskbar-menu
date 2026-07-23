//go:build windows

package main

import "unsafe"

// placement is everything TrackPopupMenuEx needs for one show.
type placement struct {
	x, y    int32
	flags   uint32
	exclude RECT
	dpi     uint32
	edge    uint32
}

// edgeInfo describes which screen edge the taskbar occupies and the strip it
// covers. The strip doubles as both the TPMPARAMS exclude rect and the source
// of the anchor coordinate, so the same values work for a docked and an
// auto-hidden taskbar.
type edgeInfo struct {
	edge    uint32
	exclude RECT
	ok      bool
}

// detectEdge derives the taskbar edge by diffing the monitor rect against its
// work area. Doing it per monitor rather than via ABM_GETTASKBARPOS matters
// because that message only ever describes the primary taskbar, while Windows
// 11 puts one on every display.
//
// When several sides are inset (other appbars docked alongside), the widest
// inset wins; ties resolve bottom, top, left, right.
func detectEdge(mon, work RECT) edgeInfo {
	insets := [4]int32{
		abeBottom: mon.Bottom - work.Bottom,
		abeTop:    work.Top - mon.Top,
		abeLeft:   work.Left - mon.Left,
		abeRight:  mon.Right - work.Right,
	}

	best := uint32(abeBottom)
	bestInset := int32(0)
	for _, e := range [4]uint32{abeBottom, abeTop, abeLeft, abeRight} {
		if insets[e] > bestInset {
			best, bestInset = e, insets[e]
		}
	}
	if bestInset <= 0 {
		return edgeInfo{}
	}
	return edgeInfo{edge: best, exclude: excludeRect(best, mon, work), ok: true}
}

func excludeRect(edge uint32, mon, work RECT) RECT {
	switch edge {
	case abeTop:
		return RECT{mon.Left, mon.Top, mon.Right, work.Top}
	case abeLeft:
		return RECT{mon.Left, mon.Top, work.Left, mon.Bottom}
	case abeRight:
		return RECT{work.Right, mon.Top, mon.Right, mon.Bottom}
	default: // abeBottom
		return RECT{mon.Left, work.Bottom, mon.Right, mon.Bottom}
	}
}

// alignFlags maps the taskbar edge to the direction the menu should grow.
// A bottom taskbar opens up-and-right, a right taskbar opens left-and-down.
func alignFlags(edge uint32) uint32 {
	switch edge {
	case abeTop:
		return tpmLeftAlign | tpmTopAlign
	case abeLeft:
		return tpmLeftAlign | tpmTopAlign
	case abeRight:
		return tpmRightAlign | tpmTopAlign
	default: // abeBottom
		return tpmLeftAlign | tpmBottomAlign
	}
}

// anchorPoint pins the axis perpendicular to the taskbar to the edge of the
// taskbar strip, so the menu sits flush against it regardless of where within
// the button the click landed, while the parallel axis follows the cursor.
// anchor=="cursor" reproduces menuApp's raw behaviour instead.
func anchorPoint(edge uint32, cursor POINT, strip RECT, mode string) (int32, int32) {
	if mode == "cursor" {
		return cursor.X, cursor.Y
	}
	switch edge {
	case abeTop:
		return cursor.X, strip.Bottom
	case abeLeft:
		return strip.Right, cursor.Y
	case abeRight:
		return strip.Left, cursor.Y
	default: // abeBottom
		return cursor.X, strip.Top
	}
}

// resolvePlacement figures out the monitor under the cursor, its DPI, and where
// and which way the menu should open.
func resolvePlacement(cursor POINT, anchorMode string) placement {
	p := placement{dpi: 96, edge: abeBottom}

	mon := monitorFromPoint(cursor, monitorDefaultToNearest)
	if mon == 0 {
		p.x, p.y = cursor.X, cursor.Y
		p.flags = alignFlags(p.edge)
		return p
	}
	p.dpi = getDpiForMonitor(mon)

	mi, ok := getMonitorInfo(mon)
	if !ok {
		p.x, p.y = cursor.X, cursor.Y
		p.flags = alignFlags(p.edge)
		return p
	}

	info := detectEdge(mi.RcMonitor, mi.RcWork)
	if !info.ok {
		// Work area fills the monitor: either no taskbar here, or it is set to
		// auto-hide. ABM_GETTASKBARPOS still reports the strip it would cover.
		info = taskbarFromAppBar(mi.RcMonitor)
	}
	if !info.ok {
		info = edgeInfo{
			edge:    abeBottom,
			exclude: RECT{mi.RcMonitor.Left, mi.RcMonitor.Bottom, mi.RcMonitor.Right, mi.RcMonitor.Bottom},
			ok:      true,
		}
	}

	p.edge = info.edge
	p.exclude = info.exclude
	p.flags = alignFlags(info.edge)
	p.x, p.y = anchorPoint(info.edge, cursor, info.exclude, anchorMode)
	return p
}

// taskbarFromAppBar is the auto-hide fallback. It is only trusted when the
// reported strip actually overlaps the monitor we care about.
func taskbarFromAppBar(mon RECT) edgeInfo {
	var abd APPBARDATA
	abd.Size = uint32(unsafe.Sizeof(abd))
	if shAppBarMessage(abmGetTaskbarPos, &abd) == 0 {
		return edgeInfo{}
	}
	if abd.Rc.Right <= mon.Left || abd.Rc.Left >= mon.Right ||
		abd.Rc.Bottom <= mon.Top || abd.Rc.Top >= mon.Bottom {
		return edgeInfo{}
	}
	return edgeInfo{edge: abd.Edge, exclude: abd.Rc, ok: true}
}
