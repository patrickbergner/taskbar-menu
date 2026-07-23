//go:build windows

// seticon stamps a .ico and version info into an already-built PE.
//
// The Go toolchain has no resource compiler, and a pinned taskbar button needs
// an icon. Rather than pull in a third-party .syso generator, this walks the
// .ico container and writes the RT_ICON / RT_GROUP_ICON resources directly with
// BeginUpdateResource, which is a documented Win32 API and needs nothing
// installed. While it has the file open it also builds and writes an RT_VERSION
// resource by hand, which is what populates the Details tab of the file's
// Properties dialog.
//
//	seticon.exe <target.exe> <icon.ico> <version>
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// Product identity stamped into every build. The exe name comes from the
// version-info fields (Description, Product name, Copyright, ...) that Windows
// Explorer shows in the Details tab of a file's Properties dialog.
const (
	productName     = "Taskbar Menu"
	fileDescription = "Taskbar Menu"
	companyName     = "Emontis"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procBeginUpdateResourceW = kernel32.NewProc("BeginUpdateResourceW")
	procUpdateResourceW      = kernel32.NewProc("UpdateResourceW")
	procEndUpdateResourceW   = kernel32.NewProc("EndUpdateResourceW")
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16

	langNeutral = 0x0409 // en-US; the loader falls back to it for any locale

	iconDirHeaderSize = 6
	iconDirEntrySize  = 16
	grpIconEntrySize  = 14
)

// iconDirEntry is one image in the .ico container. The on-disk layout ends with
// a 4-byte file offset; the RT_GROUP_ICON layout replaces that with a 2-byte
// resource id, which is the only structural difference between the two.
type iconDirEntry struct {
	width      byte
	height     byte
	colorCount byte
	reserved   byte
	planes     uint16
	bitCount   uint16
	bytesInRes uint32
	offset     uint32
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: seticon <target.exe> <icon.ico> <version>")
		os.Exit(2)
	}
	if err := stamp(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintf(os.Stderr, "seticon: %v\n", err)
		os.Exit(1)
	}
}

func stamp(exePath, icoPath, version string) error {
	raw, err := os.ReadFile(icoPath)
	if err != nil {
		return err
	}
	entries, err := parseICO(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", icoPath, err)
	}
	verData, err := buildVersionInfo(version, filepath.Base(exePath))
	if err != nil {
		return fmt.Errorf("version %q: %w", version, err)
	}

	h, err := beginUpdate(exePath)
	if err != nil {
		return fmt.Errorf("BeginUpdateResource(%s): %w", exePath, err)
	}

	// Image resources are numbered from 1; the group resource is id 1 as well,
	// in its own type, which is what the shell looks up first.
	for i, e := range entries {
		img := raw[e.offset : e.offset+e.bytesInRes]
		if err := updateResource(h, rtIcon, uint16(i+1), img); err != nil {
			discardUpdate(h)
			return fmt.Errorf("RT_ICON %d: %w", i+1, err)
		}
	}
	if err := updateResource(h, rtGroupIcon, 1, buildGroup(entries)); err != nil {
		discardUpdate(h)
		return fmt.Errorf("RT_GROUP_ICON: %w", err)
	}
	if err := updateResource(h, rtVersion, 1, verData); err != nil {
		discardUpdate(h)
		return fmt.Errorf("RT_VERSION: %w", err)
	}

	if err := endUpdate(h); err != nil {
		return fmt.Errorf("EndUpdateResource: %w", err)
	}
	fmt.Printf("seticon: wrote %d image(s) from %s and version %s into %s\n", len(entries), icoPath, version, exePath)
	return nil
}

func parseICO(raw []byte) ([]iconDirEntry, error) {
	if len(raw) < iconDirHeaderSize {
		return nil, fmt.Errorf("too short to be an icon")
	}
	if binary.LittleEndian.Uint16(raw[0:]) != 0 || binary.LittleEndian.Uint16(raw[2:]) != 1 {
		return nil, fmt.Errorf("not an ICO container")
	}
	count := int(binary.LittleEndian.Uint16(raw[4:]))
	if count == 0 {
		return nil, fmt.Errorf("contains no images")
	}
	need := iconDirHeaderSize + count*iconDirEntrySize
	if len(raw) < need {
		return nil, fmt.Errorf("directory truncated: want %d bytes, have %d", need, len(raw))
	}

	entries := make([]iconDirEntry, count)
	for i := range entries {
		b := raw[iconDirHeaderSize+i*iconDirEntrySize:]
		e := iconDirEntry{
			width:      b[0],
			height:     b[1],
			colorCount: b[2],
			reserved:   b[3],
			planes:     binary.LittleEndian.Uint16(b[4:]),
			bitCount:   binary.LittleEndian.Uint16(b[6:]),
			bytesInRes: binary.LittleEndian.Uint32(b[8:]),
			offset:     binary.LittleEndian.Uint32(b[12:]),
		}
		if uint64(e.offset)+uint64(e.bytesInRes) > uint64(len(raw)) {
			return nil, fmt.Errorf("image %d runs past end of file", i)
		}
		entries[i] = e
	}
	return entries, nil
}

// buildGroup emits the RT_GROUP_ICON directory that ties the RT_ICON images
// together, so the shell can pick the best size for a given display.
func buildGroup(entries []iconDirEntry) []byte {
	out := make([]byte, iconDirHeaderSize+len(entries)*grpIconEntrySize)
	binary.LittleEndian.PutUint16(out[0:], 0) // reserved
	binary.LittleEndian.PutUint16(out[2:], 1) // type: icon
	binary.LittleEndian.PutUint16(out[4:], uint16(len(entries)))

	for i, e := range entries {
		b := out[iconDirHeaderSize+i*grpIconEntrySize:]
		b[0], b[1], b[2], b[3] = e.width, e.height, e.colorCount, e.reserved
		binary.LittleEndian.PutUint16(b[4:], e.planes)
		binary.LittleEndian.PutUint16(b[6:], e.bitCount)
		binary.LittleEndian.PutUint32(b[8:], e.bytesInRes)
		binary.LittleEndian.PutUint16(b[12:], uint16(i+1)) // resource id
	}
	return out
}

// ---------------------------------------------------------------------------
// RT_VERSION
//
// The Go toolchain has no resource compiler, so the VS_VERSIONINFO resource
// that Explorer's Details tab reads is built by hand here, following the
// layout documented at
// https://learn.microsoft.com/windows/win32/menurc/vs-versioninfo. It is a
// tree of variable-length blocks, each starting with the same three WORDs
// (total length, value length, type) and 4-byte aligned at every level.
// ---------------------------------------------------------------------------

// buildVersionInfo assembles the VS_VERSIONINFO resource for the given
// dotted version string ("major.minor.patch[.build]", missing parts default
// to 0).
func buildVersionInfo(version, exeName string) ([]byte, error) {
	major, minor, patch, build, err := parseVersion(version)
	if err != nil {
		return nil, err
	}

	var strings_ []byte
	addString := func(key, value string) {
		strings_ = append(strings_, versionString(key, value)...)
	}
	addString("CompanyName", companyName)
	addString("FileDescription", fileDescription)
	addString("FileVersion", version)
	addString("InternalName", exeName)
	addString("LegalCopyright", fmt.Sprintf("© %s", companyName))
	addString("OriginalFilename", exeName)
	addString("ProductName", productName)
	addString("ProductVersion", version)

	// "040904B0": language 0x0409 (en-US), code page 0x04B0 (1200, Unicode) —
	// must match the langid/codepage pair in the VarFileInfo block below.
	stringTable := versionBlock("040904B0", 1, 0, nil, strings_)
	stringFileInfo := versionBlock("StringFileInfo", 1, 0, nil, stringTable)

	translation := make([]byte, 4)
	binary.LittleEndian.PutUint16(translation[0:], 0x0409)
	binary.LittleEndian.PutUint16(translation[2:], 0x04B0)
	varEntry := versionBlock("Translation", 0, uint16(len(translation)), translation, nil)
	varFileInfo := versionBlock("VarFileInfo", 1, 0, nil, varEntry)

	fixed := fixedFileInfo(major, minor, patch, build)
	children := append(append([]byte{}, stringFileInfo...), varFileInfo...)
	return versionBlock("VS_VERSION_INFO", 0, uint16(len(fixed)), fixed, children), nil
}

// versionString builds one String entry (a text key/value pair) of a
// StringTable, per the "String" layout in vs-versioninfo.
func versionString(key, value string) []byte {
	v := utf16z(value)
	return versionBlock(key, 1, uint16(len(v)/2), v, nil)
}

// versionBlock builds one node of the VS_VERSIONINFO tree: a 6-byte header
// (wLength, wValueLength, wType) followed by the null-terminated UTF-16 key,
// the value and the children, each padded to a 4-byte boundary before the
// next member starts.
func versionBlock(key string, wType, wValueLength uint16, value, children []byte) []byte {
	b := make([]byte, 6)
	b = append(b, utf16z(key)...)
	b = pad4(b)
	b = append(b, value...)
	b = pad4(b)
	b = append(b, children...)
	binary.LittleEndian.PutUint16(b[0:], uint16(len(b)))
	binary.LittleEndian.PutUint16(b[2:], wValueLength)
	binary.LittleEndian.PutUint16(b[4:], wType)
	return b
}

// fixedFileInfo builds the 52-byte VS_FIXEDFILEINFO struct that is the Value
// member of the top-level VS_VERSIONINFO block.
func fixedFileInfo(major, minor, patch, build uint16) []byte {
	b := make([]byte, 52)
	binary.LittleEndian.PutUint32(b[0:], 0xFEEF04BD)                       // dwSignature
	binary.LittleEndian.PutUint32(b[4:], 0x00010000)                       // dwStrucVersion
	binary.LittleEndian.PutUint32(b[8:], uint32(major)<<16|uint32(minor))  // dwFileVersionMS
	binary.LittleEndian.PutUint32(b[12:], uint32(patch)<<16|uint32(build)) // dwFileVersionLS
	binary.LittleEndian.PutUint32(b[16:], uint32(major)<<16|uint32(minor)) // dwProductVersionMS
	binary.LittleEndian.PutUint32(b[20:], uint32(patch)<<16|uint32(build)) // dwProductVersionLS
	binary.LittleEndian.PutUint32(b[24:], 0x3F)                            // dwFileFlagsMask
	binary.LittleEndian.PutUint32(b[28:], 0)                               // dwFileFlags
	binary.LittleEndian.PutUint32(b[32:], 0x00040004)                      // dwFileOS: VOS_NT_WINDOWS32
	binary.LittleEndian.PutUint32(b[36:], 1)                               // dwFileType: VFT_APP
	binary.LittleEndian.PutUint32(b[40:], 0)                               // dwFileSubtype
	binary.LittleEndian.PutUint32(b[44:], 0)                               // dwFileDateMS
	binary.LittleEndian.PutUint32(b[48:], 0)                               // dwFileDateLS
	return b
}

// parseVersion accepts 1-4 dot-separated decimal components, each fitting in
// a WORD; missing trailing components default to 0.
func parseVersion(s string) (major, minor, patch, build uint16, err error) {
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return 0, 0, 0, 0, fmt.Errorf("invalid version %q: want 1-4 dot-separated numbers", s)
	}
	var nums [4]uint16
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("invalid version %q: %w", s, err)
		}
		nums[i] = uint16(n)
	}
	return nums[0], nums[1], nums[2], nums[3], nil
}

// utf16z encodes s as null-terminated UTF-16LE, the string form every member
// of the VS_VERSIONINFO tree uses.
func utf16z(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, (len(u)+1)*2)
	for _, c := range u {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	return binary.LittleEndian.AppendUint16(b, 0)
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// ---------------------------------------------------------------------------
// Win32
// ---------------------------------------------------------------------------

func beginUpdate(path string) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	// bDeleteExistingResources = TRUE: a Go binary has no icon to preserve, and
	// starting clean makes re-stamping idempotent.
	h, _, e := procBeginUpdateResourceW.Call(uintptr(unsafe.Pointer(p)), 1)
	runtime.KeepAlive(p)
	if h == 0 {
		return 0, e
	}
	return syscall.Handle(h), nil
}

func updateResource(h syscall.Handle, typ uint16, id uint16, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty resource")
	}
	r, _, e := procUpdateResourceW.Call(uintptr(h),
		uintptr(typ), uintptr(id), langNeutral,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	runtime.KeepAlive(data)
	if r == 0 {
		return e
	}
	return nil
}

func endUpdate(h syscall.Handle) error {
	r, _, e := procEndUpdateResourceW.Call(uintptr(h), 0)
	if r == 0 {
		return e
	}
	return nil
}

// discardUpdate rolls back a partial update so a failure cannot leave the exe
// with half an icon written.
func discardUpdate(h syscall.Handle) {
	procEndUpdateResourceW.Call(uintptr(h), 1) // fDiscard = TRUE
}
