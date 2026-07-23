//go:build windows

package main

import (
	"testing"
	"unsafe"
)

// Every struct below is passed to Win32 with its size baked into a cbSize
// field, or is written into by the OS at a fixed layout. A wrong size or a
// missing padding word does not fail to compile -- it fails at runtime with
// ERROR_INVALID_PARAMETER, or silently corrupts memory. These are the amd64
// sizes from the Windows SDK headers.
func TestStructSizes(t *testing.T) {
	cases := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"POINT", unsafe.Sizeof(POINT{}), 8},
		{"RECT", unsafe.Sizeof(RECT{}), 16},
		{"SIZE", unsafe.Sizeof(SIZE{}), 8},
		{"GUID", unsafe.Sizeof(GUID{}), 16},
		{"MSG", unsafe.Sizeof(MSG{}), 48},
		{"WNDCLASSEXW", unsafe.Sizeof(WNDCLASSEXW{}), 80},
		{"MENUITEMINFOW", unsafe.Sizeof(MENUITEMINFOW{}), 80},
		{"MENUINFO", unsafe.Sizeof(MENUINFO{}), 40},
		{"TPMPARAMS", unsafe.Sizeof(TPMPARAMS{}), 20},
		{"MEASUREITEMSTRUCT", unsafe.Sizeof(MEASUREITEMSTRUCT{}), 32},
		{"DRAWITEMSTRUCT", unsafe.Sizeof(DRAWITEMSTRUCT{}), 64},
		{"LOGFONTW", unsafe.Sizeof(LOGFONTW{}), 92},
		{"NONCLIENTMETRICSW", unsafe.Sizeof(NONCLIENTMETRICSW{}), 504},
		{"MONITORINFO", unsafe.Sizeof(MONITORINFO{}), 40},
		{"APPBARDATA", unsafe.Sizeof(APPBARDATA{}), 48},
		{"SHELLEXECUTEINFOW", unsafe.Sizeof(SHELLEXECUTEINFOW{}), 112},
		{"SHFILEINFOW", unsafe.Sizeof(SHFILEINFOW{}), 696},
		{"NOTIFYICONDATAW", unsafe.Sizeof(NOTIFYICONDATAW{}), 976},
		{"BITMAP", unsafe.Sizeof(BITMAP{}), 32},
		{"ICONINFO", unsafe.Sizeof(ICONINFO{}), 32},
		{"BITMAPINFOHEADER", unsafe.Sizeof(BITMAPINFOHEADER{}), 40},
		{"D2D1_PIXEL_FORMAT", unsafe.Sizeof(D2D1_PIXEL_FORMAT{}), 8},
		{"D2D1_RENDER_TARGET_PROPERTIES", unsafe.Sizeof(D2D1_RENDER_TARGET_PROPERTIES{}), 28},
		{"D2D1_COLOR_F", unsafe.Sizeof(D2D1_COLOR_F{}), 16},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("sizeof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// Field offsets for the structs where a misplaced padding word would still
// produce the right total size, so TestStructSizes alone would not catch it.
func TestStructOffsets(t *testing.T) {
	var mii MENUITEMINFOW
	var dis DRAWITEMSTRUCT
	var mis MEASUREITEMSTRUCT
	var ncm NONCLIENTMETRICSW
	var nid NOTIFYICONDATAW
	var sei SHELLEXECUTEINFOW
	var abd APPBARDATA
	var bm BITMAP
	var ii ICONINFO

	cases := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"MENUITEMINFOW.SubMenu", unsafe.Offsetof(mii.SubMenu), 24},
		{"MENUITEMINFOW.ItemData", unsafe.Offsetof(mii.ItemData), 48},
		{"MENUITEMINFOW.BmpItem", unsafe.Offsetof(mii.BmpItem), 72},

		{"DRAWITEMSTRUCT.HwndItem", unsafe.Offsetof(dis.HwndItem), 24},
		{"DRAWITEMSTRUCT.RcItem", unsafe.Offsetof(dis.RcItem), 40},
		{"DRAWITEMSTRUCT.ItemData", unsafe.Offsetof(dis.ItemData), 56},

		{"MEASUREITEMSTRUCT.ItemData", unsafe.Offsetof(mis.ItemData), 24},

		{"NONCLIENTMETRICSW.MenuFont", unsafe.Offsetof(ncm.MenuFont), 224},
		{"NONCLIENTMETRICSW.PaddedBorderWidth", unsafe.Offsetof(ncm.PaddedBorderWidth), 500},

		{"NOTIFYICONDATAW.Wnd", unsafe.Offsetof(nid.Wnd), 8},
		{"NOTIFYICONDATAW.Icon", unsafe.Offsetof(nid.Icon), 32},
		{"NOTIFYICONDATAW.Tip", unsafe.Offsetof(nid.Tip), 40},
		{"NOTIFYICONDATAW.Info", unsafe.Offsetof(nid.Info), 304},
		{"NOTIFYICONDATAW.InfoTitle", unsafe.Offsetof(nid.InfoTitle), 820},
		{"NOTIFYICONDATAW.GuidItem", unsafe.Offsetof(nid.GuidItem), 952},

		{"SHELLEXECUTEINFOW.Show", unsafe.Offsetof(sei.Show), 48},
		{"SHELLEXECUTEINFOW.InstApp", unsafe.Offsetof(sei.InstApp), 56},
		{"SHELLEXECUTEINFOW.Process", unsafe.Offsetof(sei.Process), 104},

		{"APPBARDATA.Wnd", unsafe.Offsetof(abd.Wnd), 8},
		{"APPBARDATA.Rc", unsafe.Offsetof(abd.Rc), 24},

		// GetObject writes the width/height the mask bitmap is sized from, and
		// CreateIconIndirect reads the two HBITMAPs; a misplaced padding word in
		// either would corrupt the icon rather than fail loudly.
		{"BITMAP.Bits", unsafe.Offsetof(bm.Bits), 24},
		{"ICONINFO.HbmMask", unsafe.Offsetof(ii.HbmMask), 16},
		{"ICONINFO.HbmColor", unsafe.Offsetof(ii.HbmColor), 24},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("offsetof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestRGBIsBGROrdered(t *testing.T) {
	// COLORREF is 0x00BBGGRR, not 0x00RRGGBB. Getting this backwards would
	// swap red and blue in every palette.
	if got := rgb(0x12, 0x34, 0x56); got != 0x00563412 {
		t.Errorf("rgb(0x12,0x34,0x56) = %#08x, want 0x00563412", got)
	}
}

// scale drives every dimension in the menu, so the standard Windows scaling
// factors are worth pinning explicitly.
func TestScale(t *testing.T) {
	cases := []struct {
		dpi  uint32
		in   int32
		want int32
	}{
		{96, 16, 16},  // 100%
		{120, 16, 20}, // 125%
		{144, 16, 24}, // 150%
		{168, 16, 28}, // 175%
		{192, 16, 32}, // 200%
		{240, 16, 40}, // 250%
		{96, 0, 0},
	}
	for _, c := range cases {
		if got := scale(c.in, c.dpi); got != c.want {
			t.Errorf("scale(%d, %d) = %d, want %d", c.in, c.dpi, got, c.want)
		}
	}
}
