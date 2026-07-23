//go:build windows

package main

import "testing"

var fullHD = RECT{0, 0, 2560, 1440}

func TestDetectEdge(t *testing.T) {
	cases := []struct {
		name    string
		work    RECT
		wantOK  bool
		want    uint32
		exclude RECT
	}{
		{
			name: "bottom", work: RECT{0, 0, 2560, 1392},
			wantOK: true, want: abeBottom, exclude: RECT{0, 1392, 2560, 1440},
		},
		{
			name: "top", work: RECT{0, 48, 2560, 1440},
			wantOK: true, want: abeTop, exclude: RECT{0, 0, 2560, 48},
		},
		{
			name: "left", work: RECT{80, 0, 2560, 1440},
			wantOK: true, want: abeLeft, exclude: RECT{0, 0, 80, 1440},
		},
		{
			name: "right", work: RECT{0, 0, 2480, 1440},
			wantOK: true, want: abeRight, exclude: RECT{2480, 0, 2560, 1440},
		},
		{
			// No inset at all: either no taskbar on this monitor or it is set
			// to auto-hide. The caller must fall back to ABM_GETTASKBARPOS.
			name: "auto-hide or none", work: fullHD,
			wantOK: false,
		},
		{
			// A docked appbar on another edge must not outvote the taskbar;
			// the widest inset wins.
			name:   "taskbar bottom, thin appbar left",
			work:   RECT{24, 0, 2560, 1392},
			wantOK: true, want: abeBottom, exclude: RECT{0, 1392, 2560, 1440},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := detectEdge(fullHD, c.work)
			if got.ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", got.ok, c.wantOK)
			}
			if !c.wantOK {
				return
			}
			if got.edge != c.want {
				t.Errorf("edge = %d, want %d", got.edge, c.want)
			}
			if got.exclude != c.exclude {
				t.Errorf("exclude = %+v, want %+v", got.exclude, c.exclude)
			}
		})
	}
}

// A bottom taskbar must open the menu upward, a right taskbar leftward, and so
// on. TPM_LEFTALIGN and TPM_TOPALIGN are both zero, so the flags are asserted
// against their literal values rather than by name.
func TestAlignFlags(t *testing.T) {
	cases := []struct {
		edge uint32
		want uint32
	}{
		{abeBottom, tpmLeftAlign | tpmBottomAlign},
		{abeTop, tpmLeftAlign | tpmTopAlign},
		{abeLeft, tpmLeftAlign | tpmTopAlign},
		{abeRight, tpmRightAlign | tpmTopAlign},
	}
	for _, c := range cases {
		if got := alignFlags(c.edge); got != c.want {
			t.Errorf("alignFlags(%d) = %#x, want %#x", c.edge, got, c.want)
		}
	}
	if alignFlags(abeBottom)&tpmBottomAlign == 0 {
		t.Error("bottom taskbar must anchor the menu by its bottom edge so it opens upward")
	}
	if alignFlags(abeRight)&tpmRightAlign == 0 {
		t.Error("right taskbar must anchor the menu by its right edge so it opens leftward")
	}
}

func TestAnchorPoint(t *testing.T) {
	cases := []struct {
		name   string
		edge   uint32
		cursor POINT
		strip  RECT
		mode   string
		wantX  int32
		wantY  int32
	}{
		{
			// Flush to the taskbar, following the cursor horizontally: the
			// click can land anywhere in the button's height and the menu
			// still sits on the taskbar edge.
			name: "bottom/taskbar", edge: abeBottom,
			cursor: POINT{640, 1420}, strip: RECT{0, 1392, 2560, 1440},
			mode: "taskbar", wantX: 640, wantY: 1392,
		},
		{
			name: "bottom/cursor", edge: abeBottom,
			cursor: POINT{640, 1420}, strip: RECT{0, 1392, 2560, 1440},
			mode: "cursor", wantX: 640, wantY: 1420,
		},
		{
			name: "top/taskbar", edge: abeTop,
			cursor: POINT{640, 20}, strip: RECT{0, 0, 2560, 48},
			mode: "taskbar", wantX: 640, wantY: 48,
		},
		{
			name: "left/taskbar", edge: abeLeft,
			cursor: POINT{40, 700}, strip: RECT{0, 0, 80, 1440},
			mode: "taskbar", wantX: 80, wantY: 700,
		},
		{
			name: "right/taskbar", edge: abeRight,
			cursor: POINT{2520, 700}, strip: RECT{2480, 0, 2560, 1440},
			mode: "taskbar", wantX: 2480, wantY: 700,
		},
		{
			// Negative coordinates are normal for a monitor left of the
			// primary one.
			name: "secondary monitor left of primary", edge: abeBottom,
			cursor: POINT{-1200, 1000}, strip: RECT{-1920, 1032, 0, 1080},
			mode: "taskbar", wantX: -1200, wantY: 1032,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x, y := anchorPoint(c.edge, c.cursor, c.strip, c.mode)
			if x != c.wantX || y != c.wantY {
				t.Errorf("anchorPoint = (%d,%d), want (%d,%d)", x, y, c.wantX, c.wantY)
			}
		})
	}
}
