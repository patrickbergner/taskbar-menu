//go:build windows

package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsImageFile(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`C:\icons\logo.png`, true},
		{`C:\icons\logo.SVG`, true}, // case-insensitive
		{`C:\a\b.jpeg`, true},
		{`photo.avif`, true},
		{`pic.webp`, true},
		{`C:\Windows\System32\imageres.dll`, false},
		{`app.exe`, false},
		{`favorites.ico`, false}, // .ico stays on the extraction path
		{`C:\no\extension`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := isImageFile(c.in); got != c.want {
			t.Errorf("isImageFile(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// fitBox is the whole answer to "non-square images must not be squashed": the
// scaled box keeps the source aspect ratio and is centered.
func TestFitBox(t *testing.T) {
	cases := []struct {
		sw, sh, size     int32
		w, h, offX, offY int32
	}{
		{32, 32, 32, 32, 32, 0, 0}, // exact
		{16, 16, 32, 32, 32, 0, 0}, // upscaled square
		{64, 64, 32, 32, 32, 0, 0}, // downscaled square
		{40, 10, 32, 32, 8, 0, 12}, // wide: full width, letterboxed top/bottom
		{10, 40, 32, 8, 32, 12, 0}, // tall: full height, pillarboxed left/right
		{100, 50, 16, 16, 8, 0, 4}, // wide at a small size
		{0, 0, 32, 32, 32, 0, 0},   // degenerate: fill
	}
	for _, c := range cases {
		w, h, offX, offY := fitBox(c.sw, c.sh, c.size)
		if w != c.w || h != c.h || offX != c.offX || offY != c.offY {
			t.Errorf("fitBox(%d,%d,%d) = (w=%d h=%d ox=%d oy=%d), want (w=%d h=%d ox=%d oy=%d)",
				c.sw, c.sh, c.size, w, h, offX, offY, c.w, c.h, c.offX, c.offY)
		}
		// The scaled box must sit fully inside the square, or the copy would run
		// past the buffer.
		if offX < 0 || offY < 0 || offX+w > c.size || offY+h > c.size {
			t.Errorf("fitBox(%d,%d,%d) box escapes the square", c.sw, c.sh, c.size)
		}
	}
}

func TestPackSizeF(t *testing.T) {
	// width in the low 32 bits, height in the high 32 bits.
	got := packSizeF(32, 16)
	wantLo := uint32(0x42000000) // 32.0f
	wantHi := uint32(0x41800000) // 16.0f
	if uint32(got) != wantLo || uint32(got>>32) != wantHi {
		t.Errorf("packSizeF(32,16) = %#016x, want lo=%#x hi=%#x", got, wantLo, wantHi)
	}
}

// comSetup pins the goroutine to one OS thread and initializes COM on it, which
// every WIC/Direct2D call in these tests needs.
func comSetup(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	coInitialize()
}

// writePNG saves img as a PNG and returns its path.
func writePNG(t *testing.T, img image.Image) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

// alphaAt reads the alpha of pixel (x,y) from a size x size BGRA buffer.
func alphaAt(buf []byte, size, x, y int32) byte { return buf[(y*size+x)*4+3] }

// redAt reads the red channel of pixel (x,y).
func redAt(buf []byte, size, x, y int32) byte { return buf[(y*size+x)*4+2] }

// A PNG opaque only in its top-left quarter must come back opaque top-left and
// transparent bottom-right -- which pins decoding, the alpha channel, and the
// top-down orientation all at once. A flipped image would fail bottom-right.
func TestRenderRasterOrientationAndAlpha(t *testing.T) {
	comSetup(t)

	const src = 16
	img := image.NewNRGBA(image.Rect(0, 0, src, src))
	for y := 0; y < src; y++ {
		for x := 0; x < src; x++ {
			if x < src/2 && y < src/2 {
				img.Set(x, y, color.NRGBA{R: 255, A: 255}) // opaque red
			} // else left zero: transparent
		}
	}
	path := writePNG(t, img)

	const size = 32
	buf, ok := renderRaster(path, size)
	if !ok {
		t.Fatal("renderRaster failed on a PNG")
	}
	if len(buf) != size*size*4 {
		t.Fatalf("buffer is %d bytes, want %d", len(buf), size*size*4)
	}

	if a := alphaAt(buf, size, size/4, size/4); a < 200 {
		t.Errorf("top-left should be opaque, alpha=%d", a)
	}
	if r := redAt(buf, size, size/4, size/4); r < 200 {
		t.Errorf("top-left should be red, red=%d", r)
	}
	if a := alphaAt(buf, size, size*3/4, size*3/4); a > 50 {
		t.Errorf("bottom-right should be transparent, alpha=%d", a)
	}
}

// A wide, fully opaque image must be scaled to the full width and centered
// vertically with transparent bands above and below -- not stretched to fill the
// square. This is the "non-square images are squashed" regression.
func TestRenderRasterLetterboxesWideImage(t *testing.T) {
	comSetup(t)

	img := image.NewNRGBA(image.Rect(0, 0, 40, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	path := writePNG(t, img)

	const size = 32
	buf, ok := renderRaster(path, size)
	if !ok {
		t.Fatal("renderRaster failed")
	}
	// fitBox(40,10,32) -> h=8, offY=12: opaque band is rows 12..19.
	if a := alphaAt(buf, size, size/2, 2); a > 50 {
		t.Errorf("top band should be transparent, alpha=%d", a)
	}
	if a := alphaAt(buf, size, size/2, size/2); a < 200 {
		t.Errorf("middle band should be opaque, alpha=%d", a)
	}
	if a := alphaAt(buf, size, size/2, 29); a > 50 {
		t.Errorf("bottom band should be transparent, alpha=%d", a)
	}
}

// End to end through the icon cache: a PNG icon must yield a real HICON, not the
// generic fallback, and be cached per size.
func TestImageIconFromPNG(t *testing.T) {
	comSetup(t)
	defer purgeIcons()

	img := image.NewNRGBA(image.Rect(0, 0, 24, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			img.Set(x, y, color.NRGBA{G: 200, A: 255})
		}
	}
	path := writePNG(t, img)

	h := iconFor(path, 0, 32)
	if h == 0 {
		t.Fatal("no icon handle for a PNG")
	}
	if h == genericIcon() {
		t.Fatal("PNG fell back to the generic icon")
	}
	if h2 := iconFor(path, 0, 32); h2 != h {
		t.Error("repeated iconFor for a PNG must hit the cache")
	}
}

func TestImageIconMissingFileFallsBack(t *testing.T) {
	comSetup(t)
	if buf, ok := renderRaster(`C:\definitely\not\here.png`, 32); ok || buf != nil {
		t.Error("a missing image must fail, not return pixels")
	}
	if h := imageIcon(`C:\definitely\not\here.png`, 32); h != 0 {
		t.Error("imageIcon on a missing file must return 0 so iconFor falls back")
	}
}

// SVG goes through the Direct2D path, which is the part most sensitive to a
// wrong COM vtable offset. A filled square must come back opaque.
func TestRenderSVGSquare(t *testing.T) {
	comSetup(t)

	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">` +
		`<rect x="0" y="0" width="10" height="10" fill="#ff0000"/></svg>`
	path := filepath.Join(t.TempDir(), "square.svg")
	if err := os.WriteFile(path, []byte(svg), 0o644); err != nil {
		t.Fatal(err)
	}

	const size = 32
	buf, ok := renderSVG(path, size)
	if !ok {
		t.Fatal("renderSVG failed -- check the Direct2D vtable offsets")
	}
	if a := alphaAt(buf, size, size/2, size/2); a < 200 {
		t.Errorf("center of a filled SVG should be opaque, alpha=%d", a)
	}
	if r := redAt(buf, size, size/2, size/2); r < 200 {
		t.Errorf("center of a red SVG should be red, red=%d", r)
	}
}

// A 2:1 SVG rendered into a square viewport must be letterboxed by Direct2D's
// default preserveAspectRatio, not stretched.
func TestRenderSVGLetterboxes(t *testing.T) {
	comSetup(t)

	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 10">` +
		`<rect x="0" y="0" width="20" height="10" fill="#00ff00"/></svg>`
	path := filepath.Join(t.TempDir(), "wide.svg")
	if err := os.WriteFile(path, []byte(svg), 0o644); err != nil {
		t.Fatal(err)
	}

	const size = 32
	buf, ok := renderSVG(path, size)
	if !ok {
		t.Fatal("renderSVG failed")
	}
	// 2:1 into 32x32 meets at width 32, height 16, centered: opaque rows ~8..23.
	if a := alphaAt(buf, size, size/2, 2); a > 50 {
		t.Errorf("top band should be transparent, alpha=%d", a)
	}
	if a := alphaAt(buf, size, size/2, size/2); a < 200 {
		t.Errorf("middle band should be opaque, alpha=%d", a)
	}
	if a := alphaAt(buf, size, size/2, 30); a > 50 {
		t.Errorf("bottom band should be transparent, alpha=%d", a)
	}
}
