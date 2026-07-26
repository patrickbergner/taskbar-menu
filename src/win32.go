//go:build windows

// Win32 bindings. Everything here is hand-written against the amd64 ABI, so the
// struct layouts matter: a wrong cbSize fails at runtime with an opaque error
// rather than at compile time. win32_test.go asserts every size that is passed
// to the API as a cbSize.
package main

import (
	"os"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// ---------------------------------------------------------------------------
// DLLs
//
// Loaded by absolute path out of System32 so the DLL search order cannot be
// hijacked by a file dropped next to the exe. shcore in particular is not a
// KnownDLL, so plain-name loading would be unsafe.
// ---------------------------------------------------------------------------

var sysDir = func() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return root + `\System32\`
}()

func sysDLL(name string) *syscall.LazyDLL { return syscall.NewLazyDLL(sysDir + name) }

var (
	kernel32 = sysDLL("kernel32.dll")
	user32   = sysDLL("user32.dll")
	gdi32    = sysDLL("gdi32.dll")
	shell32  = sysDLL("shell32.dll")
	shcore   = sysDLL("shcore.dll")
	shlwapi  = sysDLL("shlwapi.dll")
	ole32    = sysDLL("ole32.dll")
	advapi32 = sysDLL("advapi32.dll")
	powrprof = sysDLL("powrprof.dll")

	// Imaging: WIC decodes raster formats (PNG/JPEG/GIF/BMP/TIFF, and
	// AVIF/HEIC/WebP where the OS codec is installed); Direct2D rasterizes SVG.
	// Both are reached through COM, the same route as IShellItemImageFactory.
	windowscodecs = sysDLL("windowscodecs.dll")
	d2d1          = sysDLL("d2d1.dll")

	procCreateMutexW       = kernel32.NewProc("CreateMutexW")
	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	procAttachConsole      = kernel32.NewProc("AttachConsole")
	procGetStdHandle       = kernel32.NewProc("GetStdHandle")
	procSearchPathW        = kernel32.NewProc("SearchPathW")
	procMulDiv             = kernel32.NewProc("MulDiv")
	procGetCurrentThreadID = kernel32.NewProc("GetCurrentThreadId")

	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procFindWindowW                   = user32.NewProc("FindWindowW")
	procGetWindowThreadProcessId      = user32.NewProc("GetWindowThreadProcessId")
	procAllowSetForegroundWindow      = user32.NewProc("AllowSetForegroundWindow")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procSetWindowPos                  = user32.NewProc("SetWindowPos")
	procRegisterWindowMessageW        = user32.NewProc("RegisterWindowMessageW")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
	procCreatePopupMenu               = user32.NewProc("CreatePopupMenu")
	procDestroyMenu                   = user32.NewProc("DestroyMenu")
	procInsertMenuItemW               = user32.NewProc("InsertMenuItemW")
	procDeleteMenu                    = user32.NewProc("DeleteMenu")
	procSetMenuInfo                   = user32.NewProc("SetMenuInfo")
	procTrackPopupMenuEx              = user32.NewProc("TrackPopupMenuEx")
	procMonitorFromPoint              = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	procSystemParametersInfoForDpi    = user32.NewProc("SystemParametersInfoForDpi")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procPrivateExtractIconsW          = user32.NewProc("PrivateExtractIconsW")
	procDrawIconEx                    = user32.NewProc("DrawIconEx")
	procDestroyIcon                   = user32.NewProc("DestroyIcon")
	procLoadIconW                     = user32.NewProc("LoadIconW")
	procDrawTextW                     = user32.NewProc("DrawTextW")
	procGetSystemMetricsForDpi        = user32.NewProc("GetSystemMetricsForDpi")
	procFillRect                      = user32.NewProc("FillRect")
	procMessageBoxW                   = user32.NewProc("MessageBoxW")
	procExitWindowsEx                 = user32.NewProc("ExitWindowsEx")
	procLockWorkStation               = user32.NewProc("LockWorkStation")
	procSetWindowsHookExW             = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx           = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx                = user32.NewProc("CallNextHookEx")
	procSetWindowLongPtrW             = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW               = user32.NewProc("CallWindowProcW")
	procGetWindowRect                 = user32.NewProc("GetWindowRect")
	procGetWindowDC                   = user32.NewProc("GetWindowDC")
	procGetClassNameW                 = user32.NewProc("GetClassNameW")
	procSendMessageW                  = user32.NewProc("SendMessageW")
	procGetMenuItemCount              = user32.NewProc("GetMenuItemCount")
	procGetMenuItemInfoW              = user32.NewProc("GetMenuItemInfoW")
	procGetMenuItemRect               = user32.NewProc("GetMenuItemRect")

	procCreateSolidBrush      = gdi32.NewProc("CreateSolidBrush")
	procCreateFontIndirectW   = gdi32.NewProc("CreateFontIndirectW")
	procCreateRoundRectRgn    = gdi32.NewProc("CreateRoundRectRgn")
	procCreatePolygonRgn      = gdi32.NewProc("CreatePolygonRgn")
	procFillRgn               = gdi32.NewProc("FillRgn")
	procDeleteObject          = gdi32.NewProc("DeleteObject")
	procSelectObject          = gdi32.NewProc("SelectObject")
	procSetBkMode             = gdi32.NewProc("SetBkMode")
	procSetTextColor          = gdi32.NewProc("SetTextColor")
	procGetTextExtentPoint32W = gdi32.NewProc("GetTextExtentPoint32W")
	procGetDC                 = user32.NewProc("GetDC")
	procReleaseDC             = user32.NewProc("ReleaseDC")
	procCreatePen             = gdi32.NewProc("CreatePen")
	procRectangle             = gdi32.NewProc("Rectangle")
	procGetStockObject        = gdi32.NewProc("GetStockObject")

	procShellExecuteExW             = shell32.NewProc("ShellExecuteExW")
	procShellNotifyIconW            = shell32.NewProc("Shell_NotifyIconW")
	procSHGetFileInfoW              = shell32.NewProc("SHGetFileInfoW")
	procSHAppBarMessage             = shell32.NewProc("SHAppBarMessage")
	procExtractIconExW              = shell32.NewProc("ExtractIconExW")
	procPickIconDlg                 = shell32.NewProc("PickIconDlg")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
	procSHGetLocalizedName          = shell32.NewProc("SHGetLocalizedName")

	// A string resource, read out of a module mapped for its resources alone;
	// see localizedName in shellenum.go.
	procLoadLibraryExW = kernel32.NewProc("LoadLibraryExW")
	procLoadStringW    = user32.NewProc("LoadStringW")

	procCreateIconIndirect = user32.NewProc("CreateIconIndirect")
	procCreateBitmap       = gdi32.NewProc("CreateBitmap")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procGetObjectW         = gdi32.NewProc("GetObjectW")

	procGetDpiForMonitor = shcore.NewProc("GetDpiForMonitor")

	procAssocQueryStringW     = shlwapi.NewProc("AssocQueryStringW")
	procSHCreateStreamOnFileW = shlwapi.NewProc("SHCreateStreamOnFileW")

	procCoInitializeEx    = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance  = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree     = ole32.NewProc("CoTaskMemFree")
	procD2D1CreateFactory = d2d1.NewProc("D2D1CreateFactory")

	// Shutting the machine down needs SeShutdownPrivilege turned on in this
	// process's token first; see enablePrivilege in power.go.
	procOpenProcessToken      = advapi32.NewProc("OpenProcessToken")
	procLookupPrivilegeValueW = advapi32.NewProc("LookupPrivilegeValueW")
	procAdjustTokenPrivileges = advapi32.NewProc("AdjustTokenPrivileges")
	procGetCurrentProcess     = kernel32.NewProc("GetCurrentProcess")
	procSetSuspendState       = powrprof.NewProc("SetSuspendState")
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	wsPopup        = 0x80000000
	wsExToolWindow = 0x00000080

	cwUseDefault = ^uintptr(0x7FFFFFFF) // 0x80000000 as a signed int

	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmMeasureItem   = 0x002C
	wmDrawItem      = 0x002B
	wmInitMenuPopup = 0x0117
	wmSettingChange = 0x001A
	wmNull          = 0x0000
	wmApp           = 0x8000
	wmPaint         = 0x000F
	wmNcPaint       = 0x0085
	wmNcDestroy     = 0x0082

	wmRButtonUp = 0x0205
	wmLButtonUp = 0x0202

	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	// Menu item flags.
	miimState   = 0x00000001
	miimID      = 0x00000002
	miimSubMenu = 0x00000004
	miimData    = 0x00000020
	miimFType   = 0x00000100

	mftString    = 0x00000000
	mftOwnerDraw = 0x00000100
	// mftMenuBreak starts a new column at this item. MFT_MENUBARBREAK (0x20)
	// does the same and draws a divider between the columns, but paints it in
	// hardcoded classic system colours -- the same problem the flyout arrows
	// have (see border.go), so the plain break is the only usable one here.
	mftMenuBreak = 0x00000040

	mfsEnabled  = 0x00000000
	mfsDisabled = 0x00000003
	mfsHilite   = 0x00000080

	mfByPosition = 0x00000400

	mimBackground      = 0x00000002
	mimApplyToSubMenus = 0x80000000

	tpmLeftAlign   = 0x0000
	tpmRightAlign  = 0x0008
	tpmTopAlign    = 0x0000
	tpmBottomAlign = 0x0020
	tpmLeftButton  = 0x0000
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100
	tpmWorkArea    = 0x10000

	odtMenu     = 1
	odsSelected = 0x0001
	odsDisabled = 0x0004

	// DPI.
	dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3) // (HANDLE)-4
	mdtEffectiveDPI                      = 0
	monitorDefaultToNearest              = 2
	spiGetNonClientMetrics               = 0x0029

	// AppBar.
	abmGetTaskbarPos = 0x00000005
	abeLeft          = 0
	abeTop           = 1
	abeRight         = 2
	abeBottom        = 3

	// Shell.
	shgfiIconLocation      = 0x000001000
	shgfiUseFileAttributes = 0x000000010
	fileAttributeNormal    = 0x00000080

	seeMaskNoAsync  = 0x00000100
	seeMaskFlagNoUI = 0x00000400

	// IShellItemImageFactory::GetImage flags. ICONONLY takes the item's own
	// icon rather than letting the shell synthesise a thumbnail, which is what a
	// Store app's package logo comes back as.
	siigbfIconOnly = 0x00000004

	swHide          = 0
	swShowNormal    = 1
	swShowMinimized = 2
	swShowMaximized = 3

	// Tray.
	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010

	niifInfo    = 0x00000001
	niifWarning = 0x00000002
	niifError   = 0x00000003

	smCXSmIcon = 49
	// smCXMenuCheck is the check-mark gutter Windows reserves on every menu
	// item, owner-draw or not, on top of the width WM_MEASUREITEM asked for.
	// Column layout has to add it back to know how wide a column really is; see
	// splitColumns in menu.go.
	smCXMenuCheck = 71

	// GDI.
	transparent   = 1
	dtSingleLine  = 0x00000020
	dtVCenter     = 0x00000004
	dtLeft        = 0x00000000
	dtNoPrefix    = 0x00000800
	dtEndEllipsis = 0x00008000
	diNormal      = 0x0003

	polygonAlternate = 1
	polygonWinding   = 2

	psSolid   = 0
	nullBrush = 5

	// Hooking. Used to subclass the popup-menu window created while the
	// borderHook is installed; see border.go.
	whCbt         = 5
	hcbtCreateWnd = 3
	gwlpWndProc   = ^uintptr(3) // (LONG_PTR)-4, sign-extended

	// mnGetHMenu is undocumented but stable since Windows 2000: sent to a
	// popup-menu window ("#32768") it returns the HMENU it is displaying, which
	// is otherwise not exposed for a window not owned via GetMenu.
	mnGetHMenu = 0x01E1

	idiApplication = 32512

	coinitApartmentThreaded = 0x2
	coinitDisableOLE1DDE    = 0x4

	assocstrExecutable  = 2
	assocstrDefaultIcon = 15

	mbIconError       = 0x00000010
	mbIconInformation = 0x00000040
	mbIconWarning     = 0x00000030
	mbOK              = 0x00000000
	mbYesNo           = 0x00000004
	mbDefButton2      = 0x00000100
	mbSetForeground   = 0x00010000
	mbTopMost         = 0x00040000

	idYes = 6

	// Token and privilege plumbing for ExitWindowsEx / SetSuspendState.
	tokenAdjustPrivileges = 0x0020
	tokenQuery            = 0x0008
	sePrivilegeEnabled    = 0x00000002
	errorNotAllAssigned   = syscall.Errno(1300)
	seShutdownName        = "SeShutdownPrivilege"

	ewxLogoff      = 0x00000000
	ewxShutdown    = 0x00000001
	ewxReboot      = 0x00000002
	ewxForceIfHung = 0x00000010

	// SHTDN_REASON_MAJOR_OTHER | SHTDN_REASON_MINOR_OTHER | FLAG_PLANNED.
	// Without a reason code the event log records an unplanned shutdown.
	shtdnReasonPlanned = 0x80000000

	attachParentProcess = ^uintptr(0)  // DWORD -1
	stdOutputHandle     = ^uintptr(10) // DWORD -11
	invalidHandle       = ^uintptr(0)
)

// ---------------------------------------------------------------------------
// Structs
//
// Sizes below are the amd64 sizes; win32_test.go pins each one.
// ---------------------------------------------------------------------------

type POINT struct{ X, Y int32 } // 8

type RECT struct{ Left, Top, Right, Bottom int32 } // 16

type SIZE struct{ CX, CY int32 } // 8

type GUID struct { // 16
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type LUID struct { // 8
	LowPart  uint32
	HighPart int32
}

type LUID_AND_ATTRIBUTES struct { // 12
	Luid       LUID
	Attributes uint32
}

// TOKEN_PRIVILEGES is variable-length in C; one entry is all enablePrivilege
// ever asks for, and AdjustTokenPrivileges reads only PrivilegeCount of them.
type TOKEN_PRIVILEGES struct { // 16
	PrivilegeCount uint32
	Privileges     [1]LUID_AND_ATTRIBUTES
}

type MSG struct { // 48
	Hwnd    syscall.Handle
	Message uint32
	_       uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
	_       uint32
}

type WNDCLASSEXW struct { // 80
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   syscall.Handle
	Icon       syscall.Handle
	Cursor     syscall.Handle
	Background syscall.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     syscall.Handle
}

type MENUITEMINFOW struct { // 80
	Size      uint32
	Mask      uint32
	Type      uint32
	State     uint32
	ID        uint32
	_         uint32
	SubMenu   syscall.Handle
	Checked   syscall.Handle
	Unchecked syscall.Handle
	ItemData  uintptr
	TypeData  *uint16
	Cch       uint32
	_         uint32
	BmpItem   syscall.Handle
}

type MENUINFO struct { // 40
	Size          uint32
	Mask          uint32
	Style         uint32
	YMax          uint32
	Back          syscall.Handle
	ContextHelpID uint32
	_             uint32
	MenuData      uintptr
}

type TPMPARAMS struct { // 20
	Size      uint32
	RcExclude RECT
}

type MEASUREITEMSTRUCT struct { // 32
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemWidth  uint32
	ItemHeight uint32
	_          uint32
	ItemData   uintptr
}

type DRAWITEMSTRUCT struct { // 64
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemAction uint32
	ItemState  uint32
	_          uint32
	HwndItem   syscall.Handle
	HDC        syscall.Handle
	RcItem     RECT
	ItemData   uintptr
}

type LOGFONTW struct { // 92
	Height         int32
	Width          int32
	Escapement     int32
	Orientation    int32
	Weight         int32
	Italic         byte
	Underline      byte
	StrikeOut      byte
	CharSet        byte
	OutPrecision   byte
	ClipPrecision  byte
	Quality        byte
	PitchAndFamily byte
	FaceName       [32]uint16
}

type NONCLIENTMETRICSW struct { // 504
	Size              uint32
	BorderWidth       int32
	ScrollWidth       int32
	ScrollHeight      int32
	CaptionWidth      int32
	CaptionHeight     int32
	CaptionFont       LOGFONTW
	SmCaptionWidth    int32
	SmCaptionHeight   int32
	SmCaptionFont     LOGFONTW
	MenuWidth         int32
	MenuHeight        int32
	MenuFont          LOGFONTW
	StatusFont        LOGFONTW
	MessageFont       LOGFONTW
	PaddedBorderWidth int32
}

type MONITORINFO struct { // 40
	Size      uint32
	RcMonitor RECT
	RcWork    RECT
	Flags     uint32
}

type APPBARDATA struct { // 48
	Size        uint32
	_           uint32
	Wnd         syscall.Handle
	CallbackMsg uint32
	Edge        uint32
	Rc          RECT
	LParam      int64
}

type SHELLEXECUTEINFOW struct { // 112
	Size          uint32
	Mask          uint32
	Wnd           syscall.Handle
	Verb          *uint16
	File          *uint16
	Parameters    *uint16
	Directory     *uint16
	Show          int32
	_             uint32
	InstApp       syscall.Handle
	IDList        uintptr
	Class         *uint16
	KeyClass      syscall.Handle
	HotKey        uint32
	_             uint32
	IconOrMonitor syscall.Handle
	Process       syscall.Handle
}

type SHFILEINFOW struct { // 696
	Icon        syscall.Handle
	IIcon       int32
	Attributes  uint32
	DisplayName [260]uint16
	TypeName    [80]uint16
}

type BITMAP struct { // 32; filled by GetObject so an HBITMAP's size is known
	Type       int32
	Width      int32
	Height     int32
	WidthBytes int32
	Planes     uint16
	BitsPixel  uint16
	Bits       uintptr
}

type ICONINFO struct { // 32
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	_        uint32
	HbmMask  syscall.Handle
	HbmColor syscall.Handle
}

// BITMAPINFOHEADER describes the 32bpp top-down DIB section an image icon is
// rasterized into. A negative Height means top-down, so row 0 is the top edge,
// which matches the order WIC and Direct2D write their pixels.
type BITMAPINFOHEADER struct { // 40; Size must be 40 or CreateDIBSection fails
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// iidShellItemImageFactory is IID_IShellItemImageFactory
// {bcc18b79-ba16-442f-80c4-8a59c30c463b}. SHCreateItemFromParsingName returns a
// shell item as this interface directly, so no separate QueryInterface is
// needed.
var iidShellItemImageFactory = GUID{
	0xbcc18b79, 0xba16, 0x442f,
	[8]byte{0x80, 0xc4, 0x8a, 0x59, 0xc3, 0x0c, 0x46, 0x3b},
}

type NOTIFYICONDATAW struct { // 976
	Size            uint32
	_               uint32
	Wnd             syscall.Handle
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	_               uint32
	Icon            syscall.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Timeout         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GuidItem        GUID
	BalloonIcon     syscall.Handle
}

// ---------------------------------------------------------------------------
// String helpers
//
// Every *uint16 handed to the API is kept in a local that outlives the call via
// runtime.KeepAlive. Converting to uintptr inside a variadic Call() argument
// list drops the reference as far as the compiler is concerned, so the
// KeepAlive is what actually guarantees the buffer survives.
// ---------------------------------------------------------------------------

func utf16Ptr(s string) *uint16 {
	if s == "" {
		return nil
	}
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

func utf16ToString(buf []uint16) string {
	for i, c := range buf {
		if c == 0 {
			return syscall.UTF16ToString(buf[:i])
		}
	}
	return syscall.UTF16ToString(buf)
}

// copyUTF16 writes s into dst as a NUL-terminated string, truncating if needed.
func copyUTF16(dst []uint16, s string) {
	src, err := syscall.UTF16FromString(s)
	if err != nil {
		return
	}
	if len(src) > len(dst) {
		src = src[:len(dst)]
		src[len(src)-1] = 0
	}
	copy(dst, src)
}

// ---------------------------------------------------------------------------
// Typed wrappers
// ---------------------------------------------------------------------------

func getModuleHandle() syscall.Handle {
	r, _, _ := procGetModuleHandleW.Call(0)
	return syscall.Handle(r)
}

func registerClassEx(wc *WNDCLASSEXW) uint16 {
	r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(wc)))
	return uint16(r)
}

func createWindowEx(exStyle uint32, class, title string, style uint32, x, y, w, h int32, parent, menu, inst syscall.Handle) syscall.Handle {
	cp, tp := utf16Ptr(class), utf16Ptr(title)
	r, _, _ := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(cp)),
		uintptr(unsafe.Pointer(tp)),
		uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		uintptr(parent), uintptr(menu), uintptr(inst), 0)
	runtime.KeepAlive(cp)
	runtime.KeepAlive(tp)
	return syscall.Handle(r)
}

func destroyWindow(h syscall.Handle) {
	procDestroyWindow.Call(uintptr(h))
}

func defWindowProc(hwnd, msg, wp, lp uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func getMessage(m *MSG) int32 {
	r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(m)), 0, 0, 0)
	return int32(r)
}

func translateMessage(m *MSG)    { procTranslateMessage.Call(uintptr(unsafe.Pointer(m))) }
func dispatchMessage(m *MSG)     { procDispatchMessageW.Call(uintptr(unsafe.Pointer(m))) }
func postQuitMessage(code int32) { procPostQuitMessage.Call(uintptr(code)) }

func postMessage(h syscall.Handle, msg uint32, wp, lp uintptr) bool {
	r, _, _ := procPostMessageW.Call(uintptr(h), uintptr(msg), wp, lp)
	return r != 0
}

func findWindow(class string) syscall.Handle {
	cp := utf16Ptr(class)
	r, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cp)), 0)
	runtime.KeepAlive(cp)
	return syscall.Handle(r)
}

func getWindowProcessID(h syscall.Handle) uint32 {
	var pid uint32
	procGetWindowThreadProcessId.Call(uintptr(h), uintptr(unsafe.Pointer(&pid)))
	return pid
}

func allowSetForegroundWindow(pid uint32) {
	procAllowSetForegroundWindow.Call(uintptr(pid))
}

func setForegroundWindow(h syscall.Handle) {
	procSetForegroundWindow.Call(uintptr(h))
}

func moveWindowTo(h syscall.Handle, x, y int32) {
	procSetWindowPos.Call(uintptr(h), 0, uintptr(x), uintptr(y), 0, 0,
		swpNoSize|swpNoZOrder|swpNoActivate)
}

func registerWindowMessage(name string) uint32 {
	p := utf16Ptr(name)
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
	return uint32(r)
}

func getCursorPos() POINT {
	var pt POINT
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	return pt
}

func createPopupMenu() syscall.Handle {
	r, _, _ := procCreatePopupMenu.Call()
	return syscall.Handle(r)
}

func destroyMenu(h syscall.Handle) {
	if h != 0 {
		procDestroyMenu.Call(uintptr(h))
	}
}

func insertMenuItem(menu syscall.Handle, pos uint32, mii *MENUITEMINFOW) bool {
	r, _, _ := procInsertMenuItemW.Call(uintptr(menu), uintptr(pos), 1, uintptr(unsafe.Pointer(mii)))
	return r != 0
}

// deleteMenu removes an item by position. DeleteMenu rather than RemoveMenu on
// purpose: DeleteMenu destroys an attached submenu's HMENU along with the item,
// which is what keeps repopulating a dynamic submenu from leaking the menu
// handles of the level below it.
func deleteMenu(menu syscall.Handle, pos uint32) bool {
	r, _, _ := procDeleteMenu.Call(uintptr(menu), uintptr(pos), mfByPosition)
	return r != 0
}

func setMenuInfo(menu syscall.Handle, mi *MENUINFO) bool {
	r, _, _ := procSetMenuInfo.Call(uintptr(menu), uintptr(unsafe.Pointer(mi)))
	return r != 0
}

func trackPopupMenuEx(menu syscall.Handle, flags uint32, x, y int32, owner syscall.Handle, tpm *TPMPARAMS) uint32 {
	r, _, _ := procTrackPopupMenuEx.Call(uintptr(menu), uintptr(flags),
		uintptr(x), uintptr(y), uintptr(owner), uintptr(unsafe.Pointer(tpm)))
	return uint32(r)
}

func monitorFromPoint(pt POINT, flags uint32) syscall.Handle {
	// POINT is passed by value in a single 64-bit register.
	packed := uintptr(uint32(pt.X)) | uintptr(uint32(pt.Y))<<32
	r, _, _ := procMonitorFromPoint.Call(packed, uintptr(flags))
	return syscall.Handle(r)
}

func getMonitorInfo(mon syscall.Handle) (MONITORINFO, bool) {
	var mi MONITORINFO
	mi.Size = uint32(unsafe.Sizeof(mi))
	r, _, _ := procGetMonitorInfoW.Call(uintptr(mon), uintptr(unsafe.Pointer(&mi)))
	return mi, r != 0
}

func getDpiForMonitor(mon syscall.Handle) uint32 {
	var dx, dy uint32
	r, _, _ := procGetDpiForMonitor.Call(uintptr(mon), mdtEffectiveDPI,
		uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy)))
	if r != 0 || dx == 0 { // S_OK == 0
		return 96
	}
	return dx
}

func setProcessDPIAware() {
	procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorAwareV2)
}

func systemParametersInfoForDpi(action uint32, ncm *NONCLIENTMETRICSW, dpi uint32) bool {
	r, _, _ := procSystemParametersInfoForDpi.Call(uintptr(action),
		uintptr(ncm.Size), uintptr(unsafe.Pointer(ncm)), 0, uintptr(dpi))
	return r != 0
}

func getSystemMetricsForDpi(index int32, dpi uint32) int32 {
	r, _, _ := procGetSystemMetricsForDpi.Call(uintptr(index), uintptr(dpi))
	return int32(r)
}

func mulDiv(a, b, c int32) int32 {
	r, _, _ := procMulDiv.Call(uintptr(a), uintptr(b), uintptr(c))
	return int32(r)
}

func shAppBarMessage(msg uint32, data *APPBARDATA) uintptr {
	r, _, _ := procSHAppBarMessage.Call(uintptr(msg), uintptr(unsafe.Pointer(data)))
	return r
}

func getCurrentThreadID() uint32 {
	r, _, _ := procGetCurrentThreadID.Call()
	return uint32(r)
}

func setWindowsHookEx(idHook int32, callback uintptr, threadID uint32) syscall.Handle {
	r, _, _ := procSetWindowsHookExW.Call(uintptr(idHook), callback, 0, uintptr(threadID))
	return syscall.Handle(r)
}

func unhookWindowsHookEx(h syscall.Handle) {
	if h != 0 {
		procUnhookWindowsHookEx.Call(uintptr(h))
	}
}

func callNextHookEx(h syscall.Handle, nCode, wp, lp uintptr) uintptr {
	r, _, _ := procCallNextHookEx.Call(uintptr(h), nCode, wp, lp)
	return r
}

func setWindowLongPtr(hwnd syscall.Handle, index, newLong uintptr) uintptr {
	r, _, _ := procSetWindowLongPtrW.Call(uintptr(hwnd), index, newLong)
	return r
}

func callWindowProc(prev uintptr, hwnd syscall.Handle, msg uint32, wp, lp uintptr) uintptr {
	r, _, _ := procCallWindowProcW.Call(prev, uintptr(hwnd), uintptr(msg), wp, lp)
	return r
}

func getWindowRect(hwnd syscall.Handle, rc *RECT) bool {
	r, _, _ := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(rc)))
	return r != 0
}

func getWindowDC(hwnd syscall.Handle) syscall.Handle {
	r, _, _ := procGetWindowDC.Call(uintptr(hwnd))
	return syscall.Handle(r)
}

// getClassName resolves a window's class name, atom or string alike -- the
// only reliable way to recognise a popup-menu window ("#32768"), since the
// class name Windows hands CreateWindowEx for it may be an atom rather than a
// real string pointer.
func getClassName(hwnd syscall.Handle) string {
	buf := make([]uint16, 256)
	n, _, _ := procGetClassNameW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return utf16ToString(buf[:n])
}

func sendMessage(h syscall.Handle, msg uint32, wp, lp uintptr) uintptr {
	r, _, _ := procSendMessageW.Call(uintptr(h), uintptr(msg), wp, lp)
	return r
}

func getMenuItemCount(menu syscall.Handle) int32 {
	r, _, _ := procGetMenuItemCount.Call(uintptr(menu))
	return int32(r)
}

func getMenuItemInfoByPos(menu syscall.Handle, pos uint32, mii *MENUITEMINFOW) bool {
	r, _, _ := procGetMenuItemInfoW.Call(uintptr(menu), uintptr(pos), 1, uintptr(unsafe.Pointer(mii)))
	return r != 0
}

func getMenuItemRect(hwnd, menu syscall.Handle, pos uint32) (RECT, bool) {
	var rc RECT
	r, _, _ := procGetMenuItemRect.Call(uintptr(hwnd), uintptr(menu), uintptr(pos), uintptr(unsafe.Pointer(&rc)))
	return rc, r != 0
}

// ---------------------------------------------------------------------------
// GDI
// ---------------------------------------------------------------------------

func createSolidBrush(c uint32) syscall.Handle {
	r, _, _ := procCreateSolidBrush.Call(uintptr(c))
	return syscall.Handle(r)
}

func createFontIndirect(lf *LOGFONTW) syscall.Handle {
	r, _, _ := procCreateFontIndirectW.Call(uintptr(unsafe.Pointer(lf)))
	return syscall.Handle(r)
}

func createRoundRectRgn(l, t, r_, b, w, h int32) syscall.Handle {
	r, _, _ := procCreateRoundRectRgn.Call(uintptr(l), uintptr(t), uintptr(r_), uintptr(b), uintptr(w), uintptr(h))
	return syscall.Handle(r)
}

func fillRgn(hdc, rgn, brush syscall.Handle) {
	procFillRgn.Call(uintptr(hdc), uintptr(rgn), uintptr(brush))
}

func fillRect(hdc syscall.Handle, r *RECT, brush syscall.Handle) {
	procFillRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(r)), uintptr(brush))
}

func deleteObject(h syscall.Handle) {
	if h != 0 {
		procDeleteObject.Call(uintptr(h))
	}
}

func selectObject(hdc, obj syscall.Handle) syscall.Handle {
	r, _, _ := procSelectObject.Call(uintptr(hdc), uintptr(obj))
	return syscall.Handle(r)
}

func createPen(style, width int32, colour uint32) syscall.Handle {
	r, _, _ := procCreatePen.Call(uintptr(style), uintptr(width), uintptr(colour))
	return syscall.Handle(r)
}

func rectangle(hdc syscall.Handle, left, top, right, bottom int32) {
	procRectangle.Call(uintptr(hdc), uintptr(left), uintptr(top), uintptr(right), uintptr(bottom))
}

func getStockObject(i int32) syscall.Handle {
	r, _, _ := procGetStockObject.Call(uintptr(i))
	return syscall.Handle(r)
}

func setBkMode(hdc syscall.Handle, mode int32) {
	procSetBkMode.Call(uintptr(hdc), uintptr(mode))
}

func setTextColor(hdc syscall.Handle, c uint32) {
	procSetTextColor.Call(uintptr(hdc), uintptr(c))
}

func textExtent(hdc syscall.Handle, s string) SIZE {
	var sz SIZE
	u, err := syscall.UTF16FromString(s)
	if err != nil || len(u) == 0 {
		return sz
	}
	procGetTextExtentPoint32W.Call(uintptr(hdc), uintptr(unsafe.Pointer(&u[0])),
		uintptr(len(u)-1), uintptr(unsafe.Pointer(&sz)))
	runtime.KeepAlive(u)
	return sz
}

func drawText(hdc syscall.Handle, s string, r *RECT, format uint32) {
	u, err := syscall.UTF16FromString(s)
	if err != nil || len(u) == 0 {
		return
	}
	procDrawTextW.Call(uintptr(hdc), uintptr(unsafe.Pointer(&u[0])),
		uintptr(len(u)-1), uintptr(unsafe.Pointer(r)), uintptr(format))
	runtime.KeepAlive(u)
}

// createPolygonRgn builds a filled shape as a region, which avoids having to
// select and restore a pen just to keep Polygon from outlining it in black.
func createPolygonRgn(pts []POINT, mode int32) syscall.Handle {
	if len(pts) == 0 {
		return 0
	}
	r, _, _ := procCreatePolygonRgn.Call(uintptr(unsafe.Pointer(&pts[0])),
		uintptr(len(pts)), uintptr(mode))
	runtime.KeepAlive(pts)
	return syscall.Handle(r)
}

func getDC(h syscall.Handle) syscall.Handle {
	r, _, _ := procGetDC.Call(uintptr(h))
	return syscall.Handle(r)
}

func releaseDC(h, hdc syscall.Handle) {
	procReleaseDC.Call(uintptr(h), uintptr(hdc))
}

func drawIconEx(hdc syscall.Handle, x, y int32, icon syscall.Handle, cx, cy int32) {
	procDrawIconEx.Call(uintptr(hdc), uintptr(x), uintptr(y), uintptr(icon),
		uintptr(cx), uintptr(cy), 0, 0, diNormal)
}

func destroyIcon(h syscall.Handle) {
	if h != 0 {
		procDestroyIcon.Call(uintptr(h))
	}
}

func loadDefaultIcon() syscall.Handle {
	r, _, _ := procLoadIconW.Call(0, idiApplication)
	return syscall.Handle(r)
}

// rgb builds a COLORREF (0x00BBGGRR).
func rgb(r, g, b uint8) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

// ---------------------------------------------------------------------------
// Shell
// ---------------------------------------------------------------------------

func coInitialize() {
	procCoInitializeEx.Call(0, coinitApartmentThreaded|coinitDisableOLE1DDE)
}

func shellExecute(sei *SHELLEXECUTEINFOW) (bool, syscall.Errno) {
	r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(sei)))
	if r != 0 {
		return true, 0
	}
	if errno, ok := e.(syscall.Errno); ok {
		return false, errno
	}
	return false, 0
}

func shellNotifyIcon(msg uint32, data *NOTIFYICONDATAW) bool {
	r, _, _ := procShellNotifyIconW.Call(uintptr(msg), uintptr(unsafe.Pointer(data)))
	return r != 0
}

// shellIconLocation asks the shell which file and index hold the icon for path.
// For an exe that is the exe itself; for an .xlsx it resolves to Excel's
// document icon; for a folder, Explorer's.
func shellIconLocation(path string) (string, int32, bool) {
	var sfi SHFILEINFOW
	p := utf16Ptr(path)
	r, _, _ := procSHGetFileInfoW.Call(uintptr(unsafe.Pointer(p)), fileAttributeNormal,
		uintptr(unsafe.Pointer(&sfi)), unsafe.Sizeof(sfi), shgfiIconLocation)
	runtime.KeepAlive(p)
	if r == 0 {
		return "", 0, false
	}
	loc := utf16ToString(sfi.DisplayName[:])
	if loc == "" {
		return "", 0, false
	}
	return loc, sfi.IIcon, true
}

// iShellItemImageFactory is the vtable layout of the COM interface used to
// render a shell item's image. Only Release and GetImage are called, but the
// IUnknown slots ahead of them must be present so the offsets line up. Modelling
// the vtable as a typed struct keeps the calls free of pointer arithmetic.
type iShellItemImageFactory struct {
	vtbl *struct {
		QueryInterface uintptr
		AddRef         uintptr
		Release        uintptr
		GetImage       uintptr
	}
}

// shellImageIcon renders a shell item's own image to an HICON at exactly size
// pixels, through IShellItemImageFactory::GetImage. This is how a Store
// (packaged) app -- which has no .exe or .ico to extract an icon resource from
// -- still gets its real package logo instead of the generic fallback. The
// caller's thread must have CoInitialize'd, which the server does at startup.
func shellImageIcon(parsingName string, size int32) syscall.Handle {
	name := utf16Ptr(parsingName)
	if name == nil {
		return 0
	}
	var obj unsafe.Pointer
	r, _, _ := procSHCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(name)), 0,
		uintptr(unsafe.Pointer(&iidShellItemImageFactory)),
		uintptr(unsafe.Pointer(&obj)))
	runtime.KeepAlive(name)
	if r != 0 || obj == nil { // S_OK == 0
		return 0
	}
	f := (*iShellItemImageFactory)(obj)
	defer syscall.SyscallN(f.vtbl.Release, uintptr(obj))

	// SIZE is 8 bytes, passed by value in a single register: low half CX, high
	// half CY.
	packedSize := uintptr(uint32(size)) | uintptr(uint32(size))<<32
	var hbmp syscall.Handle
	hr, _, _ := syscall.SyscallN(f.vtbl.GetImage, uintptr(obj), packedSize,
		siigbfIconOnly, uintptr(unsafe.Pointer(&hbmp)))
	if hr != 0 || hbmp == 0 {
		return 0
	}
	defer deleteObject(hbmp)
	return bitmapToIcon(hbmp)
}

// bitmapToIcon wraps a 32-bit ARGB bitmap in an HICON, so a shell-rendered image
// can be drawn and cached through the very same path as an extracted icon.
// DrawIconEx honours the colour bitmap's alpha channel; the mask is required by
// CreateIconIndirect but otherwise unused, so an all-zero one is fine. Both
// bitmaps are copied into the icon, leaving the originals to the caller.
func bitmapToIcon(color syscall.Handle) syscall.Handle {
	var bm BITMAP
	if r, _, _ := procGetObjectW.Call(uintptr(color), unsafe.Sizeof(bm),
		uintptr(unsafe.Pointer(&bm))); r == 0 || bm.Width == 0 || bm.Height == 0 {
		return 0
	}
	mask, _, _ := procCreateBitmap.Call(uintptr(bm.Width), uintptr(bm.Height), 1, 1, 0)
	if mask == 0 {
		return 0
	}
	defer deleteObject(syscall.Handle(mask))

	ii := ICONINFO{FIcon: 1, HbmMask: syscall.Handle(mask), HbmColor: color}
	r, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return syscall.Handle(r)
}

// extractIcon pulls a single icon out of an .ico, .exe or .dll at exactly the
// requested pixel size. Passing the size explicitly is what keeps icons crisp
// at every DPI instead of scaling a 16px frame up.
func extractIcon(file string, index int32, size int32) syscall.Handle {
	var icon syscall.Handle
	var id uint32
	p := utf16Ptr(file)
	procPrivateExtractIconsW.Call(uintptr(unsafe.Pointer(p)), uintptr(index),
		uintptr(size), uintptr(size),
		uintptr(unsafe.Pointer(&icon)), uintptr(unsafe.Pointer(&id)), 1, 0)
	runtime.KeepAlive(p)
	return icon
}

// iconCount reports how many icons a file contains.
func iconCount(file string) int {
	p := utf16Ptr(file)
	r, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(p)), ^uintptr(0), 0, 0, 0)
	runtime.KeepAlive(p)
	return int(int32(r))
}

// pickIconDlg shows the standard Windows "Change Icon" dialog.
func pickIconDlg(file string, index int32) (string, int32, bool) {
	buf := make([]uint16, 512)
	copyUTF16(buf, file)
	idx := index
	r, _, _ := procPickIconDlg.Call(0, uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)), uintptr(unsafe.Pointer(&idx)))
	runtime.KeepAlive(buf)
	if r == 0 {
		return "", 0, false
	}
	return utf16ToString(buf), idx, true
}

// assocQueryString looks up a registered association for an extension
// (".xlsx"), a scheme ("http") or a progid ("Folder").
func assocQueryString(what uint32, assoc string) string {
	buf := make([]uint16, 512)
	n := uint32(len(buf))
	a := utf16Ptr(assoc)
	r, _, _ := procAssocQueryStringW.Call(0, uintptr(what),
		uintptr(unsafe.Pointer(a)), 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	runtime.KeepAlive(a)
	runtime.KeepAlive(buf)
	if r != 0 { // S_OK == 0
		return ""
	}
	return utf16ToString(buf)
}

// assocExecutable resolves the registered handler executable, e.g.
// "http" -> the default browser.
func assocExecutable(assoc string) string {
	return assocQueryString(assocstrExecutable, assoc)
}

// assocDefaultIcon resolves the registered icon for a type. This is where a
// document's icon actually lives: .xlsx resolves to xlicons.exe,1 and .txt to
// imageres.dll,-102, neither of which SHGetFileInfo reports.
func assocDefaultIcon(assoc string) (string, int32, bool) {
	s := assocQueryString(assocstrDefaultIcon, assoc)
	if s == "" {
		return "", 0, false
	}
	file, idx := parseIconSpec(s)
	file = strings.Trim(file, `"`) // DefaultIcon quotes paths containing spaces
	if file == "" {
		return "", 0, false
	}
	// "%1" is the association saying "the icon is in the file itself", which is
	// no answer at all for the types that register it -- a .msc holds no icon
	// resources, which is the whole reason special.go carries a hand-picked spec
	// for every Management Console entry. Reported as an absence so the caller
	// goes on looking rather than trying to extract from a literal "%1".
	if file == "%1" {
		return "", 0, false
	}
	return file, idx, true
}

// searchPath resolves a bare exe name against PATH.
func searchPath(name string) string {
	buf := make([]uint16, 512)
	n := utf16Ptr(name)
	r, _, _ := procSearchPathW.Call(0, uintptr(unsafe.Pointer(n)), 0,
		uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])), 0)
	runtime.KeepAlive(n)
	runtime.KeepAlive(buf)
	if r == 0 {
		return ""
	}
	return utf16ToString(buf)
}

func messageBox(title, text string, flags uint32) {
	messageBoxEx(0, title, text, flags)
}

// messageBoxEx is messageBox with an owner window and the button the user chose.
// The owner matters for the power-action confirmation: without one the dialog
// can end up behind whatever had focus, which for a question about shutting the
// machine down is the worst possible place for it.
func messageBoxEx(owner syscall.Handle, title, text string, flags uint32) int32 {
	tp, xp := utf16Ptr(title), utf16Ptr(text)
	r, _, _ := procMessageBoxW.Call(uintptr(owner),
		uintptr(unsafe.Pointer(xp)), uintptr(unsafe.Pointer(tp)), uintptr(flags))
	runtime.KeepAlive(tp)
	runtime.KeepAlive(xp)
	return int32(r)
}

// attachParentConsole reconnects stdout to the launching console. The exe is
// built with -H windowsgui so it has none of its own, but --check, --list-icons
// and --pick-icon still need to print somewhere useful.
func attachParentConsole() bool {
	// When the caller redirected stdout to a pipe or a file, Go already wired
	// os.Stdout to it and there is nothing to do. Attaching to the parent
	// console and replacing os.Stdout here would send the output to the console
	// instead of to the pipe the caller is reading, which looks exactly like
	// the program printing nothing.
	if h, _, _ := procGetStdHandle.Call(stdOutputHandle); h != 0 && h != invalidHandle {
		return true
	}

	r, _, _ := procAttachConsole.Call(attachParentProcess)
	if r == 0 {
		return false
	}
	h, err := syscall.CreateFile(utf16Ptr(`CONOUT$`),
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return false
	}
	os.Stdout = os.NewFile(uintptr(h), "CONOUT$")
	os.Stderr = os.Stdout
	return true
}
