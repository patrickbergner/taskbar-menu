//go:build windows

// Image icons. An entry's icon may be an ordinary image file -- SVG, PNG, JPEG,
// GIF, BMP, TIFF, and (where the OS codec is installed) AVIF, HEIC or WebP --
// instead of an icon resource. Raster formats are decoded and scaled by the
// Windows Imaging Component; SVG is rasterized by Direct2D. Both end at the same
// 32bpp premultiplied-BGRA DIB that bitmapToIcon wraps into an HICON, so an
// image icon is drawn and cached through the identical path as an extracted one.
//
// Everything here is reached through COM by hand, the same technique win32.go
// already uses for IShellItemImageFactory. A COM object's first word points at
// its vtable; comCall indexes that table by slot. The slot numbers below are the
// method's position in the interface, counting the three IUnknown methods and
// every inherited method ahead of it -- a wrong slot calls the wrong function,
// so each is documented against the interface it belongs to.
package main

import (
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// imageExts are the file types routed to the imaging stack rather than to icon
// extraction. .ico is deliberately absent: PrivateExtractIcons already picks the
// best frame out of an icon file, which WIC would not.
var imageExts = map[string]bool{
	".svg": true,
	".png": true,
	".jpg": true, ".jpeg": true, ".jpe": true, ".jfif": true,
	".gif": true,
	".bmp": true, ".dib": true,
	".tif": true, ".tiff": true,
	".webp": true,
	".avif": true, ".heic": true, ".heif": true,
	".jxr": true, ".wdp": true,
}

// isImageFile reports whether path names an image to be rendered as an icon.
func isImageFile(path string) bool {
	return imageExts[strings.ToLower(filepath.Ext(path))]
}

// imageIcon rasterizes an image file to an HICON at exactly size pixels,
// preserving the image's aspect ratio: a non-square image is scaled to fit and
// centered on a transparent square rather than stretched. Returns 0 on any
// failure -- a missing file, an unreadable image, or an absent codec (AVIF
// without the AV1 extension) -- so iconFor falls back to the generic icon
// exactly as it does for a missing .dll.
func imageIcon(path string, size int32) syscall.Handle {
	if size <= 0 {
		size = 16
	}
	var buf []byte
	var ok bool
	if strings.EqualFold(filepath.Ext(path), ".svg") {
		buf, ok = renderSVG(path, size)
	} else {
		buf, ok = renderRaster(path, size)
	}
	if !ok {
		return 0
	}
	return iconFromBGRA(buf, size)
}

// iconFromBGRA copies a size x size premultiplied-BGRA buffer into a DIB section
// and wraps it in an HICON. The DIB is deleted once CreateIconIndirect has
// copied out of it; the returned icon owns its own pixels.
func iconFromBGRA(buf []byte, size int32) syscall.Handle {
	hbmp, bits := createDIB32(size)
	if hbmp == 0 {
		return 0
	}
	defer deleteObject(hbmp)
	copy(unsafe.Slice((*byte)(bits), len(buf)), buf)
	return bitmapToIcon(hbmp)
}

// ---------------------------------------------------------------------------
// COM vtable slots
// ---------------------------------------------------------------------------

const (
	comQueryInterface = 0 // IUnknown
	comAddRef         = 1
	comRelease        = 2

	// IWICImagingFactory
	wicCreateDecoderFromFilename = 3
	wicCreateFormatConverter     = 10
	wicCreateBitmapScaler        = 11
	wicCreateBitmap              = 17

	// IWICBitmapDecoder
	wicGetFrame = 13

	// IWICBitmapSource (also IWICBitmapFrameDecode, ...Scaler, ...FormatConverter,
	// IWICBitmap, all of which derive from it)
	wicGetSize    = 3
	wicCopyPixels = 7

	// IWICBitmapScaler / IWICFormatConverter: Initialize is the first method
	// each adds after IWICBitmapSource's eight.
	wicScalerInitialize    = 8
	wicConverterInitialize = 8

	// ID2D1Factory
	d2dCreateWicBitmapRenderTarget = 13

	// ID2D1RenderTarget (its 57 methods, indices 0-56, end with these three)
	d2dClear     = 47
	d2dBeginDraw = 48
	d2dEndDraw   = 49

	// ID2D1DeviceContext5. Its two SVG methods sit past every method of
	// RenderTarget (57) and DeviceContext..4 (35+3+11+2+7), i.e. at index 115.
	d2dCreateSvgDocument = 115
	d2dDrawSvgDocument   = 116
)

// ---------------------------------------------------------------------------
// COM helpers
// ---------------------------------------------------------------------------

// comCall invokes method slot on a COM object. The receiver (this) is prepended
// automatically; args are the remaining parameters as machine words. A struct
// small enough to travel in a register (D2D1_SIZE_F) is passed as its packed
// bits; anything larger is passed by pointer, so no floating-point argument ever
// needs an XMM register, which syscall cannot supply.
func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	// The object's first word points at its vtable, a contiguous array of method
	// pointers. Reading it as *[…]uintptr indexes by slot without any uintptr
	// arithmetic, which vet rejects. 256 covers the deepest slot used (Direct2D's
	// DrawSvgDocument at 116).
	vtbl := (*[256]uintptr)(*(*unsafe.Pointer)(obj))
	all := make([]uintptr, 1, len(args)+1)
	all[0] = uintptr(obj)
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(vtbl[slot], all...)
	return r
}

// comRelease drops a reference, tolerating a nil so call sites can defer it
// unconditionally.
func release(obj unsafe.Pointer) {
	if obj != nil {
		comCall(obj, comRelease)
	}
}

// comQI asks obj for another interface, returning nil if it is not supported.
func comQI(obj unsafe.Pointer, iid *GUID) unsafe.Pointer {
	var out unsafe.Pointer
	hr := comCall(obj, comQueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if hr != 0 || out == nil {
		return nil
	}
	return out
}

// coCreateInstance creates an in-process COM object. COM must already be
// initialized on the calling thread, which the server does at startup.
func coCreateInstance(clsid, iid *GUID) unsafe.Pointer {
	var out unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(clsid)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if hr != 0 || out == nil {
		return nil
	}
	return out
}

// wicFactory creates an imaging factory. Not cached: an image icon is extracted
// once and then served from the HICON cache, so the factory's lifetime is a
// single decode and it never crosses threads.
func wicFactory() unsafe.Pointer {
	return coCreateInstance(&clsidWICImagingFactory, &iidWICImagingFactory)
}

// ---------------------------------------------------------------------------
// DIB
// ---------------------------------------------------------------------------

// createDIB32 allocates a size x size, 32bpp, top-down DIB section and returns
// its handle together with a pointer to its pixel memory. The memory is
// zero-filled by the OS, i.e. fully transparent, which is exactly the letterbox
// background a non-square image is centered on.
func createDIB32(size int32) (syscall.Handle, unsafe.Pointer) {
	bi := BITMAPINFOHEADER{
		Size:        uint32(unsafe.Sizeof(BITMAPINFOHEADER{})),
		Width:       size,
		Height:      -size, // negative: top-down
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}
	var bits unsafe.Pointer
	r, _, _ := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)),
		dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	runtime.KeepAlive(bi)
	if r == 0 || bits == nil {
		return 0, nil
	}
	return syscall.Handle(r), bits
}

// fitBox scales a sw x sh image to the largest w x h that fits inside a size x
// size square without changing its aspect ratio, and returns the top-left offset
// that centers it. This is what keeps a wide or tall image from being squashed.
func fitBox(sw, sh, size int32) (w, h, offX, offY int32) {
	if sw <= 0 || sh <= 0 {
		return size, size, 0, 0
	}
	s := math.Min(float64(size)/float64(sw), float64(size)/float64(sh))
	w = int32(math.Round(float64(sw) * s))
	h = int32(math.Round(float64(sh) * s))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if w > size {
		w = size
	}
	if h > size {
		h = size
	}
	return w, h, (size - w) / 2, (size - h) / 2
}

// ---------------------------------------------------------------------------
// WIC (raster formats)
// ---------------------------------------------------------------------------

// renderRaster decodes a raster image, high-quality-scales it to fit size x size
// without distorting its aspect ratio, and centers it on a transparent square.
// The result is a size*size*4 premultiplied-BGRA buffer. ok is false on any
// failure, including a format whose codec is not installed.
func renderRaster(path string, size int32) (buf []byte, ok bool) {
	factory := wicFactory()
	if factory == nil {
		logf("image: no WIC factory")
		return nil, false
	}
	defer release(factory)

	p := utf16Ptr(path)
	var decoder unsafe.Pointer
	hr := comCall(factory, wicCreateDecoderFromFilename,
		uintptr(unsafe.Pointer(p)), 0, genericRead, wicCacheOnDemand,
		uintptr(unsafe.Pointer(&decoder)))
	runtime.KeepAlive(p)
	if hr != 0 || decoder == nil {
		logf("image: decode %s failed hr=%#x", path, hr)
		return nil, false
	}
	defer release(decoder)

	var frame unsafe.Pointer
	if hr := comCall(decoder, wicGetFrame, 0, uintptr(unsafe.Pointer(&frame))); hr != 0 || frame == nil {
		return nil, false
	}
	defer release(frame)

	var srcW, srcH uint32
	if hr := comCall(frame, wicGetSize, uintptr(unsafe.Pointer(&srcW)), uintptr(unsafe.Pointer(&srcH))); hr != 0 || srcW == 0 || srcH == 0 {
		return nil, false
	}
	dstW, dstH, offX, offY := fitBox(int32(srcW), int32(srcH), size)

	var scaler unsafe.Pointer
	if hr := comCall(factory, wicCreateBitmapScaler, uintptr(unsafe.Pointer(&scaler))); hr != 0 || scaler == nil {
		return nil, false
	}
	defer release(scaler)
	if hr := comCall(scaler, wicScalerInitialize, uintptr(frame),
		uintptr(dstW), uintptr(dstH), wicInterpolationHighQualityCubic); hr != 0 {
		return nil, false
	}

	// Convert to premultiplied BGRA, the one format DrawIconEx alpha-blends
	// correctly. The alpha-threshold argument is a double; it is the sixth
	// parameter and therefore passed on the stack, so its zero value is simply a
	// zero word -- no XMM register involved.
	var conv unsafe.Pointer
	if hr := comCall(factory, wicCreateFormatConverter, uintptr(unsafe.Pointer(&conv))); hr != 0 || conv == nil {
		return nil, false
	}
	defer release(conv)
	if hr := comCall(conv, wicConverterInitialize, uintptr(scaler),
		uintptr(unsafe.Pointer(&guidWICPixelFormat32bppPBGRA)),
		wicDitherNone, 0, 0, wicPaletteTypeCustom); hr != 0 {
		return nil, false
	}

	// Write straight into the centered region of the output. WIC copies exactly
	// dstW*4 bytes per row at stride intervals, so the surrounding letterbox
	// stays the zero (transparent) it was allocated as. The offset region fits
	// because fitBox guarantees offX+dstW <= size and offY+dstH <= size.
	buf = make([]byte, int(size)*int(size)*4)
	stride := size * 4
	start := offY*stride + offX*4
	if hr := comCall(conv, wicCopyPixels, 0, uintptr(stride), uintptr(int32(len(buf))-start),
		uintptr(unsafe.Pointer(&buf[start]))); hr != 0 {
		runtime.KeepAlive(buf)
		return nil, false
	}
	runtime.KeepAlive(buf)
	return buf, true
}

// ---------------------------------------------------------------------------
// Direct2D (SVG)
// ---------------------------------------------------------------------------

// renderSVG rasterizes an SVG to size x size through a Direct2D WIC-bitmap
// render target and returns the size*size*4 premultiplied-BGRA pixels. A square
// viewport plus the SVG default preserveAspectRatio ("xMidYMid meet")
// letterboxes a non-square document for us. ok is false on any failure.
func renderSVG(path string, size int32) (buf []byte, ok bool) {
	factory := wicFactory()
	if factory == nil {
		logf("image: no WIC factory")
		return nil, false
	}
	defer release(factory)

	var wicBmp unsafe.Pointer
	if hr := comCall(factory, wicCreateBitmap, uintptr(size), uintptr(size),
		uintptr(unsafe.Pointer(&guidWICPixelFormat32bppPBGRA)), wicCacheOnLoad,
		uintptr(unsafe.Pointer(&wicBmp))); hr != 0 || wicBmp == nil {
		return nil, false
	}
	defer release(wicBmp)

	d2d := d2d1CreateFactory(&iidD2D1Factory)
	if d2d == nil {
		logf("image: no Direct2D factory")
		return nil, false
	}
	defer release(d2d)

	props := D2D1_RENDER_TARGET_PROPERTIES{
		PixelFormat: D2D1_PIXEL_FORMAT{Format: dxgiFormatB8G8R8A8Unorm, AlphaMode: d2dAlphaPremultiplied},
		DpiX:        96,
		DpiY:        96,
	}
	var rt unsafe.Pointer
	hr := comCall(d2d, d2dCreateWicBitmapRenderTarget, uintptr(wicBmp),
		uintptr(unsafe.Pointer(&props)), uintptr(unsafe.Pointer(&rt)))
	runtime.KeepAlive(props)
	if hr != 0 || rt == nil {
		return nil, false
	}
	defer release(rt)

	// SVG rendering lives on ID2D1DeviceContext5. Every render target is a device
	// context from Windows 8 on, and the SVG methods arrived in the Creators
	// Update, so this succeeds on Windows 11.
	dc5 := comQI(rt, &iidD2D1DeviceContext5)
	if dc5 == nil {
		logf("image: ID2D1DeviceContext5 unavailable")
		return nil, false
	}
	defer release(dc5)

	stream := shCreateStreamOnFile(path)
	if stream == nil {
		logf("image: cannot open %s", path)
		return nil, false
	}
	defer release(stream)

	viewport := packSizeF(float32(size), float32(size))
	var svg unsafe.Pointer
	if hr := comCall(dc5, d2dCreateSvgDocument, uintptr(stream), viewport,
		uintptr(unsafe.Pointer(&svg))); hr != 0 || svg == nil {
		logf("image: CreateSvgDocument %s hr=%#x", path, hr)
		return nil, false
	}
	defer release(svg)

	comCall(dc5, d2dBeginDraw)
	var clear D2D1_COLOR_F // zero: transparent
	comCall(dc5, d2dClear, uintptr(unsafe.Pointer(&clear)))
	comCall(dc5, d2dDrawSvgDocument, uintptr(svg))
	if hr := comCall(dc5, d2dEndDraw, 0, 0); hr != 0 {
		logf("image: EndDraw %s hr=%#x", path, hr)
		return nil, false
	}

	buf = make([]byte, int(size)*int(size)*4)
	stride := size * 4
	if hr := comCall(wicBmp, wicCopyPixels, 0, uintptr(stride), uintptr(len(buf)),
		uintptr(unsafe.Pointer(&buf[0]))); hr != 0 {
		runtime.KeepAlive(buf)
		return nil, false
	}
	runtime.KeepAlive(buf)
	return buf, true
}

// d2d1CreateFactory creates a single-threaded Direct2D factory.
func d2d1CreateFactory(iid *GUID) unsafe.Pointer {
	var out unsafe.Pointer
	hr, _, _ := procD2D1CreateFactory.Call(d2d1FactoryTypeSingleThreaded,
		uintptr(unsafe.Pointer(iid)), 0, uintptr(unsafe.Pointer(&out)))
	if hr != 0 || out == nil {
		return nil
	}
	return out
}

// shCreateStreamOnFile opens a read-only IStream over a file, the input
// CreateSvgDocument parses.
func shCreateStreamOnFile(path string) unsafe.Pointer {
	p := utf16Ptr(path)
	var stm unsafe.Pointer
	hr, _, _ := procSHCreateStreamOnFileW.Call(uintptr(unsafe.Pointer(p)),
		stgmRead, uintptr(unsafe.Pointer(&stm)))
	runtime.KeepAlive(p)
	if hr != 0 || stm == nil {
		return nil
	}
	return stm
}

// packSizeF packs a D2D1_SIZE_F (two 32-bit floats) into the single machine word
// it is passed in: width in the low half, height in the high half.
func packSizeF(w, h float32) uintptr {
	return uintptr(math.Float32bits(w)) | uintptr(math.Float32bits(h))<<32
}

// ---------------------------------------------------------------------------
// COM constants, GUIDs and structs used only by the imaging path
// ---------------------------------------------------------------------------

const (
	clsctxInprocServer = 0x1
	genericRead        = 0x80000000
	stgmRead           = 0x0

	biRGB        = 0
	dibRGBColors = 0

	// WIC.
	wicCacheOnDemand                 = 0x1
	wicCacheOnLoad                   = 0x2
	wicInterpolationHighQualityCubic = 4
	wicDitherNone                    = 0
	wicPaletteTypeCustom             = 0

	// Direct2D.
	d2d1FactoryTypeSingleThreaded = 0
	dxgiFormatB8G8R8A8Unorm       = 87
	d2dAlphaPremultiplied         = 1
)

// D2D1_PIXEL_FORMAT pairs a DXGI format with an alpha mode.
type D2D1_PIXEL_FORMAT struct { // 8
	Format    uint32
	AlphaMode uint32
}

// D2D1_RENDER_TARGET_PROPERTIES configures the WIC-bitmap render target. Passed
// by pointer, so only the field layout matters, not a size field.
type D2D1_RENDER_TARGET_PROPERTIES struct { // 28
	Type        uint32
	PixelFormat D2D1_PIXEL_FORMAT
	DpiX        float32
	DpiY        float32
	Usage       uint32
	MinLevel    uint32
}

// D2D1_COLOR_F is straight (non-premultiplied) RGBA; all-zero is transparent.
type D2D1_COLOR_F struct { // 16
	R, G, B, A float32
}

var (
	// CLSID_WICImagingFactory {cacaf262-9370-4615-a13b-9f5539da4c0a}
	clsidWICImagingFactory = GUID{0xcacaf262, 0x9370, 0x4615,
		[8]byte{0xa1, 0x3b, 0x9f, 0x55, 0x39, 0xda, 0x4c, 0x0a}}
	// IID_IWICImagingFactory {ec5ec8a9-c395-4314-9c77-54d7a935ff70}
	iidWICImagingFactory = GUID{0xec5ec8a9, 0xc395, 0x4314,
		[8]byte{0x9c, 0x77, 0x54, 0xd7, 0xa9, 0x35, 0xff, 0x70}}
	// GUID_WICPixelFormat32bppPBGRA {6fddc324-4e03-4bfe-b185-3d77768dc910}
	guidWICPixelFormat32bppPBGRA = GUID{0x6fddc324, 0x4e03, 0x4bfe,
		[8]byte{0xb1, 0x85, 0x3d, 0x77, 0x76, 0x8d, 0xc9, 0x10}}
	// IID_ID2D1Factory {06152247-6f50-465a-9245-118bfd3b6007}
	iidD2D1Factory = GUID{0x06152247, 0x6f50, 0x465a,
		[8]byte{0x92, 0x45, 0x11, 0x8b, 0xfd, 0x3b, 0x60, 0x07}}
	// IID_ID2D1DeviceContext5 {7836d248-68cc-4df6-b9e8-de991bf62eb7}
	iidD2D1DeviceContext5 = GUID{0x7836d248, 0x68cc, 0x4df6,
		[8]byte{0xb9, 0xe8, 0xde, 0x99, 0x1b, 0xf6, 0x2e, 0xb7}}
)
