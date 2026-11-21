//go:build windows && desktop

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/webview2"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

const (
	configWMClose       = 0x0010
	configWMSetIcon     = 0x0080
	configWMTimer       = 0x0113
	configWMSave        = 0x8001
	configWMOpenBrowser = 0x8002
)

var (
	configUser32          = windows.NewLazySystemDLL("user32.dll")
	configRegisterClass   = configUser32.NewProc("RegisterClassW")
	configUnregisterClass = configUser32.NewProc("UnregisterClassW")
	configCreateWindow    = configUser32.NewProc("CreateWindowExW")
	configDestroyWindow   = configUser32.NewProc("DestroyWindow")
	configDefWindowProc   = configUser32.NewProc("DefWindowProcW")
	configGetMessage      = configUser32.NewProc("GetMessageW")
	configTranslate       = configUser32.NewProc("TranslateMessage")
	configDispatch        = configUser32.NewProc("DispatchMessageW")
	configPostMessage     = configUser32.NewProc("PostMessageW")
	configSendMessage     = configUser32.NewProc("SendMessageW")
	configPostQuitMessage = configUser32.NewProc("PostQuitMessage")
	configCreateIcon      = configUser32.NewProc("CreateIconFromResourceEx")
	configDestroyIcon     = configUser32.NewProc("DestroyIcon")
	configGetClientRect   = configUser32.NewProc("GetClientRect")
	configShowWindow      = configUser32.NewProc("ShowWindow")
	configSetTimer        = configUser32.NewProc("SetTimer")
	configKillTimer       = configUser32.NewProc("KillTimer")
)

type configWindowClass struct {
	style                   uint32
	proc                    uintptr
	classExtra, windowExtra int32
	instance, icon, cursor  uintptr
	background              uintptr
	menuName, className     *uint16
}

type configWindowMessage struct {
	window         uintptr
	message        uint32
	wParam, lParam uintptr
	time           uint32
	point          webview2.POINT
	private        uint32
}

type windowsConfigWindow struct {
	hwnd       uintptr
	origin     *url.URL
	folder     string
	controller *webview2.ICoreWebView2Controller
	browser    windows.Handle
	callbacks  []*configCOMCallback
	external   *uint16
	download   *configWindowsDownload
	saveDialog uintptr
	saving     bool
	closing    bool
	err        error
}

func init() {
	if standaloneConfigApp == "1" || (len(os.Args) > 2 && os.Args[1] == "config" && os.Args[2] == "gui") {
		runtime.LockOSThread()
	}
}

func runConfigGUI(args []string) error {
	var window windowsConfigWindow
	err := runDesktopWorkspace(args, window.open)
	if window.folder != "" {
		err = errors.Join(err, window.removeWebViewData())
	}
	return err
}

func (w *windowsConfigWindow) open(ctx context.Context, address string) error {
	var err error
	w.origin, err = url.Parse(address)
	if err != nil {
		return err
	}
	w.folder, err = os.MkdirTemp("", "t3-bridge-webview2-")
	if err != nil {
		return err
	}

	err = windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	if err != nil && err != syscall.Errno(windows.S_FALSE) {
		return fmt.Errorf("initialize WebView2 COM apartment: %w", err)
	}
	defer windows.CoUninitialize()

	if err := w.create(); err != nil {
		return err
	}
	// Win32 accepts PNG data directly as an icon resource.
	const iconResourceVersion = 0x00030000
	icon, _, _ := configCreateIcon.Call(uintptr(unsafe.Pointer(&workspaceIcon[0])), uintptr(len(workspaceIcon)), 1, iconResourceVersion, 0, 0, 0)
	if icon != 0 {
		defer configDestroyIcon.Call(icon)
		const (
			iconSmall = 0
			iconBig   = 1
		)
		configSendMessage.Call(w.hwnd, configWMSetIcon, iconSmall, icon)
		configSendMessage.Call(w.hwnd, configWMSetIcon, iconBig, icon)
	}
	defer w.destroy()
	if ctx.Err() != nil {
		return nil
	}
	hwnd := w.hwnd
	cancelPosted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		configPostMessage.Call(hwnd, configWMClose, 0, 0)
		close(cancelPosted)
	})
	defer func() {
		if !stop() {
			<-cancelPosted
		}
	}()

	timer, _, timerErr := configSetTimer.Call(hwnd, 1, 20000, 0)
	if timer == 0 {
		return fmt.Errorf("start WebView2 initialization timer: %w", timerErr)
	}
	err = webviewloader.CreateCoreWebView2EnvironmentWithOptions(w,
		webviewloader.WithUserDataFolder(w.folder),
		webviewloader.WithExclusiveUserDataFolderAccess(true),
		webviewloader.WithAdditionalBrowserArguments("--no-proxy-server"),
	)
	if err != nil {
		return fmt.Errorf("start WebView2 (Microsoft Edge WebView2 Runtime is required): %w", err)
	}
	return w.run()
}

func (w *windowsConfigWindow) create() error {
	var instance windows.Handle
	if err := windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, nil, &instance); err != nil {
		return err
	}
	name, _ := windows.UTF16PtrFromString("T3BridgeConfigWindow")
	class := configWindowClass{
		proc:       windows.NewCallback(w.windowProc),
		instance:   uintptr(instance),
		background: 6, // COLOR_WINDOW + 1
		className:  name,
	}
	atom, _, err := configRegisterClass.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return fmt.Errorf("register configuration window: %w", err)
	}
	title, _ := windows.UTF16PtrFromString("T3 Bridge")
	const (
		overlappedWindow = 0x00cf0000
		useDefault       = 0x80000000
	)
	hwnd, _, err := configCreateWindow.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(title)),
		overlappedWindow, useDefault, useDefault, 500, 410, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		configUnregisterClass.Call(uintptr(unsafe.Pointer(name)), uintptr(instance))
		return fmt.Errorf("create configuration window: %w", err)
	}
	w.hwnd = hwnd
	return nil
}

func (w *windowsConfigWindow) run() error {
	for {
		var message configWindowMessage
		result, _, err := configGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		switch int32(result) {
		case -1:
			return fmt.Errorf("read configuration window message: %w", err)
		case 0:
			return w.err
		}
		if message.window == w.hwnd {
			switch message.message {
			case configWMSave:
				w.saveDownload()
				continue
			case configWMOpenBrowser:
				if target := w.external; target != nil && !w.closing {
					w.external = nil
					if err := windows.ShellExecute(windows.Handle(w.hwnd), nil, target, nil, nil, 1); err != nil {
						fmt.Fprintln(os.Stderr, "open Feishu authorization in system browser:", err)
					}
				}
				continue
			}
		}
		configTranslate.Call(uintptr(unsafe.Pointer(&message)))
		configDispatch.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func (w *windowsConfigWindow) windowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case configWMClose:
		w.closing = true
		if w.saving {
			if w.saveDialog != 0 {
				configPostMessage.Call(w.saveDialog, configWMClose, 0, 0)
			}
		} else {
			configPostQuitMessage.Call(0)
		}
		return 0
	case configWMTimer:
		if wParam == 1 && w.controller == nil && !w.closing {
			w.fail(errors.New("WebView2 initialization timed out"))
		}
		return 0
	case 0x0005: // WM_SIZE
		if w.controller != nil {
			_ = w.resize()
		}
	case 0x0003: // WM_MOVE
		if w.controller != nil {
			_ = w.controller.NotifyParentWindowPositionChanged()
		}
	case 0x0007: // WM_SETFOCUS
		if w.controller != nil {
			_ = w.controller.MoveFocus(webview2.COREWEBVIEW2_MOVE_FOCUS_REASON_PROGRAMMATIC)
		}
	}
	result, _, _ := configDefWindowProc.Call(hwnd, message, wParam, lParam)
	return result
}

func (w *windowsConfigWindow) fail(err error) {
	if w.err != nil || w.closing {
		return
	}
	w.err = err
	configPostMessage.Call(w.hwnd, configWMClose, 0, 0)
}

func (w *windowsConfigWindow) resize() error {
	var bounds webview2.RECT
	result, _, err := configGetClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&bounds)))
	if result == 0 {
		return err
	}
	controller := w.controller
	object := uintptr(unsafe.Pointer(controller))
	var resultHR uintptr
	switch runtime.GOARCH {
	case "386":
		resultHR, _, _ = controller.Vtbl.PutBounds.Call(object, uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right), uintptr(bounds.Bottom))
	case "arm64":
		words := (*[2]uintptr)(unsafe.Pointer(&bounds))
		resultHR, _, _ = controller.Vtbl.PutBounds.Call(object, words[0], words[1])
	default:
		return controller.PutBounds(bounds)
	}
	if resultHR != 0 {
		return syscall.Errno(resultHR)
	}
	return nil
}

func (w *windowsConfigWindow) destroy() {
	if w.download != nil {
		w.download.finish("")
		w.download = nil
	}
	if controller := w.controller; controller != nil {
		w.controller = nil
		_ = controller.Close()
		windowsConfigRelease(unsafe.Pointer(controller))
	}
	configDestroyWindow.Call(w.hwnd)
	// WebView2 calls the Go implementations through native COM pointers.
	runtime.KeepAlive(w.callbacks)
}

func (w *windowsConfigWindow) removeWebViewData() error {
	// WebView2 may still hold the profile and renderer files after the controller closes.
	deadline := time.Now().Add(5 * time.Second)
	if w.browser != 0 {
		_, _ = windows.WaitForSingleObject(w.browser, 4500)
		_ = windows.CloseHandle(w.browser)
	}
	for {
		err := os.RemoveAll(w.folder)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("remove temporary WebView2 data: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
