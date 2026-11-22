//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework AppKit -framework WebKit
#include <stdlib.h>
void open_config_window(const char *address, int menu_bar, const void *icon, size_t icon_size);
void close_config_window(void);
int workspace_uses_chinese(void);
*/
import "C"

import (
	"context"
	_ "embed"
	"os"
	"runtime"
	"unsafe"
)

//go:embed t3-bridge-macos-icon.png
var macWorkspaceIcon []byte

func platformWorkspaceLanguage() workspaceLanguage {
	if C.workspace_uses_chinese() != 0 {
		return workspaceZH
	}
	return workspaceEN
}

func init() {
	if standaloneConfigApp == "1" || (len(os.Args) > 2 && os.Args[1] == "config" && os.Args[2] == "gui") {
		// AppKit's event loop must run on the initial OS thread.
		runtime.LockOSThread()
	}
}

func runConfigGUI(args []string) error {
	return runDesktopWorkspace(args, func(workspaceCtx context.Context, address string) error {
		url := C.CString(address)
		defer C.free(unsafe.Pointer(url))
		cancelClose := context.AfterFunc(workspaceCtx, func() { C.close_config_window() })
		defer cancelClose()
		var menuBar C.int
		if standaloneConfigApp == "1" {
			menuBar = 1
		}
		C.open_config_window(url, menuBar, unsafe.Pointer(&macWorkspaceIcon[0]), C.size_t(len(macWorkspaceIcon)))
		return nil
	})
}
