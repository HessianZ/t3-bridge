//go:build windows && desktop

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/webview2"
	"golang.org/x/sys/windows"
)

var (
	configCommonDialog = windows.NewLazySystemDLL("comdlg32.dll")
	configGetSaveName  = configCommonDialog.NewProc("GetSaveFileNameW")
	configDialogError  = configCommonDialog.NewProc("CommDlgExtendedError")
	configGetParent    = configUser32.NewProc("GetParent")
)

type configOpenFileName struct {
	size                         uint32
	owner, instance              uintptr
	filter, customFilter         *uint16
	maxCustomFilter, filterIndex uint32
	file                         *uint16
	maxFile                      uint32
	fileTitle                    *uint16
	maxFileTitle                 uint32
	initialDir, title            *uint16
	flags                        uint32
	fileOffset, extensionOffset  uint16
	defaultExtension             *uint16
	customData                   *windowsConfigWindow
	hook                         uintptr
	templateName                 *uint16
	reserved                     uintptr
	reservedFlags, flagsEx       uint32
}

type configWindowsDownload struct {
	args     *webview2.ICoreWebView2DownloadStartingEventArgs
	deferral *webview2.ICoreWebView2Deferral
	name     string
}

func (w *windowsConfigWindow) watchDownloads(view *webview2.ICoreWebView2) error {
	view4 := view.GetICoreWebView2_4()
	if view4 == nil {
		return errors.New("update Microsoft Edge WebView2 Runtime to use the configuration window")
	}
	interfacePtr := unsafe.Pointer(view4)
	defer windowsConfigRelease(interfacePtr)
	// v1.0.23's _4 vtable omits inherited methods. WebView2.h defines
	// add_DownloadStarting at zero-based slot 75, including IUnknown.
	const addDownloadStarting = 75
	vtbl := *(**[addDownloadStarting + 1]webview2.ComProc)(interfacePtr)
	callback := w.callback("efedc989-c396-41ca-83f7-07f845a55724", func(_ uintptr, value unsafe.Pointer) uintptr {
		return windowsConfigHRESULT(w.downloadStarting((*webview2.ICoreWebView2DownloadStartingEventArgs)(value)))
	})
	var token webview2.EventRegistrationToken
	result, _, _ := vtbl[addDownloadStarting].Call(uintptr(interfacePtr), uintptr(callback), uintptr(unsafe.Pointer(&token)))
	if result != 0 {
		return fmt.Errorf("register WebView2 download handler: %w", syscall.Errno(result))
	}
	return nil
}

func (w *windowsConfigWindow) downloadStarting(args *webview2.ICoreWebView2DownloadStartingEventArgs) error {
	if err := configWebViewBool(args.Vtbl.PutCancel, unsafe.Pointer(args), true); err != nil {
		return err
	}
	if w.closing || w.download != nil {
		return nil
	}
	operation, err := args.GetDownloadOperation()
	if err != nil {
		return err
	}
	defer windowsConfigRelease(unsafe.Pointer(operation))

	raw, err := configWebViewString(operation.Vtbl.GetUri, unsafe.Pointer(operation))
	if err != nil {
		return nil
	}
	target := w.workspaceURL(raw)
	if target == nil {
		return nil
	}
	mime, err := configWebViewString(operation.Vtbl.GetMimeType, unsafe.Pointer(operation))
	if err != nil || mime != "application/json" {
		return nil
	}
	var name string
	switch target.Path {
	case "/":
		name = "t3-bridge.candidate.json"
	case "/profiles/export":
		profile := target.Query().Get("name")
		if !serviceInstanceName.MatchString(profile) {
			return nil
		}
		name = profile + ".json"
	default:
		return nil
	}

	deferral, err := args.GetDeferral()
	if err != nil {
		return err
	}
	args.AddRef()
	w.download = &configWindowsDownload{args, deferral, name}
	// Start the modal dialog from the host message loop after the WebView2 callback returns.
	if posted, _, err := configPostMessage.Call(w.hwnd, configWMSave, 0, 0); posted == 0 {
		w.download.finish("")
		w.download = nil
		return err
	}
	return nil
}

func (w *windowsConfigWindow) saveDownload() {
	if w.download == nil || w.closing {
		return
	}
	w.saving = true
	path, err := w.selectDownloadPath(w.download.name)
	w.saving = false
	w.saveDialog = 0
	if w.closing {
		path = ""
	}
	if err := errors.Join(err, w.download.finish(path)); err != nil {
		fmt.Fprintln(os.Stderr, "save configuration export:", err)
	}
	w.download = nil
	if w.closing {
		configPostQuitMessage.Call(0)
	}
}

func (download *configWindowsDownload) finish(path string) error {
	defer windowsConfigRelease(unsafe.Pointer(download.args))
	defer windowsConfigRelease(unsafe.Pointer(download.deferral))

	var err error
	if path != "" {
		err = download.args.PutResultFilePath(path)
		if err == nil {
			err = configWebViewBool(download.args.Vtbl.PutHandled, unsafe.Pointer(download.args), true)
		}
		if err == nil {
			err = configWebViewBool(download.args.Vtbl.PutCancel, unsafe.Pointer(download.args), false)
		}
	}
	return errors.Join(err, download.deferral.Complete())
}

func configSaveDialogHook(hwnd uintptr, message uintptr, _ uintptr, data unsafe.Pointer) uintptr {
	if message == 0x0110 { // WM_INITDIALOG
		// In Explorer-style dialogs the hook receives a child of the actual dialog.
		window := (*configOpenFileName)(data).customData
		window.saveDialog, _, _ = configGetParent.Call(hwnd)
		if window.closing {
			configPostMessage.Call(window.saveDialog, configWMClose, 0, 0)
		}
	}
	return 0
}

var configSaveDialogHookProc = windows.NewCallback(configSaveDialogHook)

func (w *windowsConfigWindow) selectDownloadPath(name string) (string, error) {
	file := make([]uint16, 32768)
	copy(file, utf16.Encode([]rune(name)))
	filter := utf16.Encode([]rune("JSON (*.json)\x00*.json\x00\x00"))
	title, _ := windows.UTF16PtrFromString("保存配置 JSON")
	extension, _ := windows.UTF16PtrFromString("json")
	const (
		overwritePrompt = 0x00000002
		noChangeDir     = 0x00000008
		enableHook      = 0x00000020
		pathMustExist   = 0x00000800
		explorer        = 0x00080000
		noAddToRecent   = 0x02000000
	)
	dialog := configOpenFileName{
		size:             uint32(unsafe.Sizeof(configOpenFileName{})),
		owner:            w.hwnd,
		filter:           &filter[0],
		filterIndex:      1,
		file:             &file[0],
		maxFile:          uint32(len(file)),
		title:            title,
		flags:            overwritePrompt | noChangeDir | enableHook | pathMustExist | explorer | noAddToRecent,
		defaultExtension: extension,
		customData:       w,
		hook:             configSaveDialogHookProc,
	}
	if result, _, _ := configGetSaveName.Call(uintptr(unsafe.Pointer(&dialog))); result == 0 {
		if code, _, _ := configDialogError.Call(); code != 0 {
			return "", fmt.Errorf("Windows save dialog error 0x%04x", code)
		}
		return "", nil
	}
	path := windows.UTF16ToString(file)
	if !filepath.IsAbs(path) {
		return "", errors.New("Windows save dialog did not return an absolute path")
	}
	return path, nil
}
