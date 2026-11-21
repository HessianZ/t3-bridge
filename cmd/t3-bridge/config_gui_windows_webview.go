//go:build windows && desktop

package main

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"syscall"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/webview2"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

type configCOMCallback struct {
	vtbl   *configCOMCallbackVTable
	iid    webview2.GUID
	invoke func(uintptr, unsafe.Pointer) uintptr
	refs   uintptr
}

type configCOMCallbackVTable struct {
	webview2.IUnknownVtbl
	invoke webview2.ComProc
}

var (
	configIIDUnknown     = webview2.NewGUID("00000000-0000-0000-c000-000000000046")
	configCallbackVTable = configCOMCallbackVTable{
		IUnknownVtbl: webview2.IUnknownVtbl{
			QueryInterface: webview2.NewComProc(func(c *configCOMCallback, iid *webview2.GUID, out *unsafe.Pointer) uintptr {
				if *iid != c.iid && *iid != *configIIDUnknown {
					*out = nil
					return uintptr(windows.E_NOINTERFACE)
				}
				*out = unsafe.Pointer(c)
				c.refs++
				return 0
			}),
			AddRef: webview2.NewComProc(func(c *configCOMCallback) uintptr {
				c.refs++
				return c.refs
			}),
			Release: webview2.NewComProc(func(c *configCOMCallback) uintptr {
				c.refs--
				return c.refs
			}),
		},
		invoke: webview2.NewComProc(func(c *configCOMCallback, sender uintptr, args unsafe.Pointer) uintptr {
			return c.invoke(sender, args)
		}),
	}
)

func (w *windowsConfigWindow) callback(iid string, invoke func(uintptr, unsafe.Pointer) uintptr) unsafe.Pointer {
	callback := &configCOMCallback{&configCallbackVTable, *webview2.NewGUID(iid), invoke, 1}
	w.callbacks = append(w.callbacks, callback)
	return unsafe.Pointer(callback)
}

func windowsConfigRelease(ptr unsafe.Pointer) {
	(*webview2.IUnknown)(ptr).Vtbl.CallRelease(ptr)
}

// The pinned binding gets the indirection wrong for some strings and BOOLs.
// WebView2.h specifies LPWSTR* getters and by-value BOOL setters.
func configWebViewString(proc webview2.ComProc, object unsafe.Pointer) (string, error) {
	var value *uint16
	result, _, _ := proc.Call(uintptr(object), uintptr(unsafe.Pointer(&value)))
	if result != 0 {
		return "", syscall.Errno(result)
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(value))
	return windows.UTF16PtrToString(value), nil
}

func configWebViewBool(proc webview2.ComProc, object unsafe.Pointer, value bool) error {
	var flag uintptr
	if value {
		flag = 1
	}
	result, _, _ := proc.Call(uintptr(object), flag)
	if result != 0 {
		return syscall.Errno(result)
	}
	return nil
}

func windowsConfigHRESULT(err error) uintptr {
	if err == nil {
		return 0
	}
	var code syscall.Errno
	if errors.As(err, &code) {
		return uintptr(code)
	}
	return uintptr(windows.E_FAIL)
}

func (w *windowsConfigWindow) EnvironmentCompleted(code webviewloader.HRESULT, environment *webviewloader.ICoreWebView2Environment) webviewloader.HRESULT {
	if code < 0 || environment == nil {
		w.fail(fmt.Errorf("create WebView2 environment: HRESULT 0x%08x", uint32(code)))
		return 0
	}
	if w.closing {
		return 0
	}

	callback := w.callback("6c4819f3-c9b7-4260-8127-c9f5bde7f68c", func(code uintptr, value unsafe.Pointer) uintptr {
		if int32(code) < 0 || value == nil {
			w.fail(fmt.Errorf("create WebView2 controller: HRESULT 0x%08x", uint32(code)))
			return 0
		}
		w.controller = (*webview2.ICoreWebView2Controller)(value)
		w.controller.AddRef()
		configKillTimer.Call(w.hwnd, 1)
		if err := w.configureWebView(); err != nil {
			w.fail(fmt.Errorf("configure WebView2: %w", err))
		}
		return 0
	})
	env := (*webview2.ICoreWebView2Environment)(unsafe.Pointer(environment))
	// The generated binding passes the address of the HWND. WebView2 takes the handle by value.
	result, _, _ := env.Vtbl.CreateCoreWebView2Controller.Call(
		uintptr(unsafe.Pointer(env)), w.hwnd, uintptr(callback),
	)
	if int32(result) < 0 {
		w.fail(fmt.Errorf("start WebView2 controller: HRESULT 0x%08x", uint32(result)))
	}
	return 0
}

func (w *windowsConfigWindow) configureWebView() error {
	view, err := w.controller.GetCoreWebView2()
	if err != nil {
		return err
	}
	defer windowsConfigRelease(unsafe.Pointer(view))
	if pid, err := view.GetBrowserProcessId(); err == nil {
		w.browser, _ = windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	}
	settings, err := view.GetSettings()
	if err != nil {
		return err
	}
	defer windowsConfigRelease(unsafe.Pointer(settings))
	for _, disable := range []webview2.ComProc{
		settings.Vtbl.PutAreDevToolsEnabled,
		settings.Vtbl.PutAreDefaultContextMenusEnabled,
		settings.Vtbl.PutAreHostObjectsAllowed,
		settings.Vtbl.PutIsWebMessageEnabled,
	} {
		if err := configWebViewBool(disable, unsafe.Pointer(settings), false); err != nil {
			return err
		}
	}
	if err := w.restrictWebView(view); err != nil {
		return err
	}
	if err := w.resize(); err != nil {
		return err
	}
	if err := view.Navigate(w.origin.String()); err != nil {
		return err
	}
	if err := configWebViewBool(w.controller.Vtbl.PutIsVisible, unsafe.Pointer(w.controller), true); err != nil {
		return err
	}
	configShowWindow.Call(w.hwnd, 5) // SW_SHOW
	return nil
}

func (w *windowsConfigWindow) restrictWebView(view *webview2.ICoreWebView2) error {
	nav := w.callback("9adbe429-f36d-432b-9ddc-f8881fbd76e3", func(_ uintptr, value unsafe.Pointer) uintptr {
		args := (*webview2.ICoreWebView2NavigationStartingEventArgs)(value)
		raw, err := configWebViewString(args.Vtbl.GetUri, value)
		if err == nil && w.workspaceURL(raw) != nil {
			return 0
		}
		return windowsConfigHRESULT(configWebViewBool(args.Vtbl.PutCancel, value, true))
	})
	if _, err := view.AddNavigationStarting((*webview2.ICoreWebView2NavigationStartingEventHandler)(nav)); err != nil {
		return err
	}
	if _, err := view.AddFrameNavigationStarting((*webview2.ICoreWebView2NavigationStartingEventHandler)(nav)); err != nil {
		return err
	}
	popup := w.callback("d4c185fe-c81c-4989-97af-2d3fa7ab5651", func(_ uintptr, value unsafe.Pointer) uintptr {
		return windowsConfigHRESULT(w.openAuthorization(view, (*webview2.ICoreWebView2NewWindowRequestedEventArgs)(value)))
	})
	if _, err := view.AddNewWindowRequested((*webview2.ICoreWebView2NewWindowRequestedEventHandler)(popup)); err != nil {
		return err
	}
	permission := w.callback("15e1c6a3-c72a-4df3-91d7-d097fbec6bfd", func(_ uintptr, value unsafe.Pointer) uintptr {
		args := (*webview2.ICoreWebView2PermissionRequestedEventArgs)(value)
		return windowsConfigHRESULT(args.PutState(webview2.COREWEBVIEW2_PERMISSION_STATE_DENY))
	})
	if _, err := view.AddPermissionRequested((*webview2.ICoreWebView2PermissionRequestedEventHandler)(permission)); err != nil {
		return err
	}
	failure := w.callback("79e0aea4-990b-42d9-aa1d-0fcc2e5bc7f1", func(_ uintptr, value unsafe.Pointer) uintptr {
		args := (*webview2.ICoreWebView2ProcessFailedEventArgs)(value)
		if kind, err := args.GetProcessFailedKind(); err == nil &&
			(kind == webview2.COREWEBVIEW2_PROCESS_FAILED_KIND_BROWSER_PROCESS_EXITED ||
				kind == webview2.COREWEBVIEW2_PROCESS_FAILED_KIND_RENDER_PROCESS_EXITED) && !w.closing {
			w.fail(errors.New("WebView2 configuration window process exited"))
		}
		return 0
	})
	if _, err := view.AddProcessFailed((*webview2.ICoreWebView2ProcessFailedEventHandler)(failure)); err != nil {
		return err
	}
	return w.watchDownloads(view)
}

func (w *windowsConfigWindow) workspaceURL(raw string) *url.URL {
	target, err := url.Parse(raw)
	if err != nil || target.User != nil || target.Scheme != w.origin.Scheme || target.Host != w.origin.Host {
		return nil
	}
	return target
}

func (w *windowsConfigWindow) openAuthorization(view *webview2.ICoreWebView2, args *webview2.ICoreWebView2NewWindowRequestedEventArgs) error {
	if err := configWebViewBool(args.Vtbl.PutHandled, unsafe.Pointer(args), true); err != nil {
		return err
	}
	clicked, err := args.GetIsUserInitiated()
	if err != nil || !clicked || w.closing {
		return nil
	}
	source, err := configWebViewString(view.Vtbl.GetSource, unsafe.Pointer(view))
	if err != nil || w.workspaceURL(source) == nil {
		return nil
	}
	raw, err := configWebViewString(args.Vtbl.GetUri, unsafe.Pointer(args))
	if err != nil {
		return err
	}
	target, err := url.Parse(raw)
	if err != nil || target.Hostname() == "" || target.User != nil {
		return nil
	}
	localOAuth := target.Scheme == "http" && target.Path == "/oauth/mcp/authorize" &&
		slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, target.Hostname())
	if target.Scheme != "https" && !localOAuth {
		return nil
	}
	w.external, err = windows.UTF16PtrFromString(target.String())
	if err != nil {
		return err
	}
	configPostMessage.Call(w.hwnd, configWMOpenBrowser, 0, 0)
	return nil
}
