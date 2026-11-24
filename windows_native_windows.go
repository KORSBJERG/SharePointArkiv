package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	registerClass        = user32.NewProc("RegisterClassExW")
	createWindow         = user32.NewProc("CreateWindowExW")
	defaultWindowProc    = user32.NewProc("DefWindowProcW")
	destroyWindow        = user32.NewProc("DestroyWindow")
	showWindow           = user32.NewProc("ShowWindow")
	getMessage           = user32.NewProc("GetMessageW")
	isDialogMessage      = user32.NewProc("IsDialogMessageW")
	translateMessage     = user32.NewProc("TranslateMessage")
	dispatchMessage      = user32.NewProc("DispatchMessageW")
	postQuitMessage      = user32.NewProc("PostQuitMessage")
	postMessage          = user32.NewProc("PostMessageW")
	sendMessage          = user32.NewProc("SendMessageW")
	setWindowText        = user32.NewProc("SetWindowTextW")
	getWindowText        = user32.NewProc("GetWindowTextW")
	getWindowTextLength  = user32.NewProc("GetWindowTextLengthW")
	enableWindow         = user32.NewProc("EnableWindow")
	setFocus             = user32.NewProc("SetFocus")
	loadCursor           = user32.NewProc("LoadCursorW")
	loadIcon             = user32.NewProc("LoadIconW")
	loadImage            = user32.NewProc("LoadImageW")
	messageBox           = user32.NewProc("MessageBoxW")
	adjustWindowRect     = user32.NewProc("AdjustWindowRectEx")
	systemParametersInfo = user32.NewProc("SystemParametersInfoW")
	setProcessDPIAware   = user32.NewProc("SetProcessDPIAware")
	getDpiForSystem      = user32.NewProc("GetDpiForSystem")
	startWindowTimer     = user32.NewProc("SetTimer")
	stopWindowTimer      = user32.NewProc("KillTimer")
	fillRect             = user32.NewProc("FillRect")

	getSysColor        = user32.NewProc("GetSysColor")
	getSysColorBrush   = user32.NewProc("GetSysColorBrush")
	setBackgroundColor = gdi32.NewProc("SetBkColor")
	setTextColor       = gdi32.NewProc("SetTextColor")
	createSolidBrush   = gdi32.NewProc("CreateSolidBrush")

	getModuleHandle = kernel32.NewProc("GetModuleHandleW")
	attachConsole   = kernel32.NewProc("AttachConsole")
	createFont      = gdi32.NewProc("CreateFontW")
	deleteObject    = gdi32.NewProc("DeleteObject")
	getStockObject  = gdi32.NewProc("GetStockObject")

	browseForFolder   = shell32.NewProc("SHBrowseForFolderW")
	getPathFromPIDL   = shell32.NewProc("SHGetPathFromIDListW")
	getPathFromPIDLEx = shell32.NewProc("SHGetPathFromIDListEx")
	shellExecute      = shell32.NewProc("ShellExecuteW")
	coInitializeEx    = ole32.NewProc("CoInitializeEx")
	coUninitialize    = ole32.NewProc("CoUninitialize")
	coTaskMemFree     = ole32.NewProc("CoTaskMemFree")
)

const (
	wsChild           = 0x40000000
	wsVisible         = 0x10000000
	wsTabStop         = 0x00010000
	wsVScroll         = 0x00200000
	wsCaption         = 0x00c00000
	wsSysMenu         = 0x00080000
	wsMinimizeBox     = 0x00020000
	wsExClientEdge    = 0x00000200
	wsExControlParent = 0x00010000

	wmClose           = 0x0010
	wmDestroy         = 0x0002
	wmCommand         = 0x0111
	wmSetFont         = 0x0030
	wmAppWork         = 0x8001
	wmTimer           = 0x0113
	wmEraseBackground = 0x0014

	wmControlColorButton = 0x0135
	wmControlColorStatic = 0x0138

	emSetLimitText = 0x00c5
	emSetSelection = 0x00b1
	emScrollCaret  = 0x00b7
	stmSetIcon     = 0x0170
	ssIcon         = 0x0003
	ssRight        = 0x0002
	emSetCueBanner = 0x1501
	bmGetCheck     = 0x00f0
	bmSetCheck     = 0x00f1
	bsAutoCheckBox = 3
	esAutoHScroll  = 0x0080
	esAutoVScroll  = 0x0040
	esMultiline    = 0x0004
	esReadOnly     = 0x0800
	esNumber       = 0x2000
)

type point struct{ x, y int32 }
type rectangle struct{ left, top, right, bottom int32 }

type windowClass struct {
	size, style                        uint32
	windowProc                         uintptr
	classExtra, windowExtra            int32
	instance, icon, cursor, background uintptr
	menu, name                         *uint16
	smallIcon                          uintptr
}

type windowMessage struct {
	window         uintptr
	message        uint32
	wParam, lParam uintptr
	time           uint32
	point          point
	private        uint32
}

func wide(s string) *uint16 {
	ptr, _ := syscall.UTF16PtrFromString(s)
	return ptr
}

func windowText(handle uintptr) string {
	size, _, _ := getWindowTextLength.Call(handle)
	if size == 0 {
		return ""
	}
	text := make([]uint16, size+1)
	getWindowText.Call(handle, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)))
	return syscall.UTF16ToString(text)
}

func setText(handle uintptr, text string) {
	setWindowText.Call(handle, uintptr(unsafe.Pointer(wide(text))))
}

func setEnabled(handle uintptr, enabled bool) {
	value := uintptr(0)
	if enabled {
		value = 1
	}
	enableWindow.Call(handle, value)
}

func createDesktopWindow(instance uintptr, class, title string, exStyle, style uintptr, area rectangle, parent, id uintptr) (uintptr, error) {
	handle, _, err := createWindow.Call(exStyle, uintptr(unsafe.Pointer(wide(class))), uintptr(unsafe.Pointer(wide(title))),
		style, uintptr(area.left), uintptr(area.top), uintptr(area.right-area.left), uintptr(area.bottom-area.top),
		parent, id, instance, 0)
	if handle == 0 {
		return 0, fmt.Errorf("opret vindue %s: %w", class, err)
	}
	return handle, nil
}

func useWindowColors(deviceContext uintptr) uintptr {
	const colorWindow = 5
	background, _, _ := getSysColor.Call(colorWindow)
	setBackgroundColor.Call(deviceContext, background)
	brush, _, _ := getSysColorBrush.Call(colorWindow)
	return brush
}

func rgb(red, green, blue uintptr) uintptr {
	return red | green<<8 | blue<<16
}

func highContrastEnabled() bool {
	var mode struct {
		size, flags uint32
		scheme      *uint16
	}
	mode.size = uint32(unsafe.Sizeof(mode))
	ok, _, _ := systemParametersInfo.Call(0x0042, uintptr(mode.size), uintptr(unsafe.Pointer(&mode)), 0)
	return ok != 0 && mode.flags&1 != 0
}

func showDesktopError(err error) {
	messageBox.Call(0, uintptr(unsafe.Pointer(wide(err.Error()))),
		uintptr(unsafe.Pointer(wide(applicationName))), 0x10)
}

func confirm(parent uintptr, text string) bool {
	id, _, _ := messageBox.Call(parent, uintptr(unsafe.Pointer(wide(text))), uintptr(unsafe.Pointer(wide(applicationName))),
		0x00000004|0x00000030|0x00000100)
	return id == 6
}

func newUIFont(face string, size, weight int) (uintptr, bool) {
	font, _, _ := createFont.Call(uintptr(-size), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(wide(face))))
	if font != 0 {
		return font, true
	}
	font, _, _ = getStockObject.Call(17)
	return font, false
}

func centerWindow(width, height int32, style, exStyle uintptr) (rectangle, int32) {
	var work rectangle
	if ok, _, _ := systemParametersInfo.Call(0x0030, 0, uintptr(unsafe.Pointer(&work)), 0); ok == 0 {
		work = rectangle{right: 1024, bottom: 768}
	}
	dpi := int32(96)
	if getDpiForSystem.Find() == nil {
		result, _, _ := getDpiForSystem.Call()
		if result != 0 {
			dpi = int32(result)
		}
	}
	dpi = min(dpi, (work.right-work.left-20)*96/width, (work.bottom-work.top-56)*96/height)
	dpi = max(dpi, 56)
	frame := rectangle{right: width * dpi / 96, bottom: height * dpi / 96}
	adjustWindowRect.Call(uintptr(unsafe.Pointer(&frame)), style, 0, exStyle)
	windowWidth, windowHeight := frame.right-frame.left, frame.bottom-frame.top
	x := work.left + (work.right-work.left-windowWidth)/2
	y := work.top + (work.bottom-work.top-windowHeight)/2
	return rectangle{x, y, x + windowWidth, y + windowHeight}, dpi
}

type browseInfo struct {
	owner, root           uintptr
	displayName, title    *uint16
	flags                 uint32
	callback, initialPath uintptr
	image                 int32
}

var browseInitialCallback = syscall.NewCallback(func(window, message, lParam, path uintptr) uintptr {
	if message == 1 && path != 0 {
		sendMessage.Call(window, 0x467, 1, path) // BFFM_SETSELECTIONW
	}
	return 0
})

func chooseFolder(parent uintptr, initial string) (string, error) {
	title := wide("Vælg den lokale kildemappe")
	initialPath := wide(initial)
	var displayName [260]uint16
	info := browseInfo{
		owner: parent, title: title, displayName: &displayName[0],
		flags: 0x0001 | 0x0040, callback: browseInitialCallback,
		initialPath: uintptr(unsafe.Pointer(initialPath)),
	}
	pidl, _, _ := browseForFolder.Call(uintptr(unsafe.Pointer(&info)))
	runtime.KeepAlive(initialPath)
	if pidl == 0 {
		return "", nil
	}
	defer coTaskMemFree.Call(pidl)
	path := make([]uint16, 32768)
	var ok uintptr
	if getPathFromPIDLEx.Find() == nil {
		ok, _, _ = getPathFromPIDLEx.Call(pidl, uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)), 0)
	} else {
		ok, _, _ = getPathFromPIDL.Call(pidl, uintptr(unsafe.Pointer(&path[0])))
	}
	if ok == 0 {
		return "", errors.New("Den valgte mappe har ingen lokal filsti.")
	}
	return syscall.UTF16ToString(path), nil
}

func shellOpen(parent uintptr, target, args string) error {
	result, _, _ := shellExecute.Call(parent, uintptr(unsafe.Pointer(wide("open"))), uintptr(unsafe.Pointer(wide(target))),
		uintptr(unsafe.Pointer(wide(args))), 0, 1)
	if result <= 32 {
		return fmt.Errorf("Windows kunne ikke åbne filen (fejlkode %d).", result)
	}
	return nil
}

func attachParentConsole() {
	if ok, _, _ := attachConsole.Call(0xffffffff); ok == 0 {
		return
	}
	if stdout, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE); err == nil {
		os.Stdout = os.NewFile(uintptr(stdout), "/dev/stdout")
	}
	if stderr, err := syscall.GetStdHandle(syscall.STD_ERROR_HANDLE); err == nil {
		os.Stderr = os.NewFile(uintptr(stderr), "/dev/stderr")
	}
}
