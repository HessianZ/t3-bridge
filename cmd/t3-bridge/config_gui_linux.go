//go:build linux && desktop && cgo

package main

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include <stdlib.h>
int open_linux_config_window(const char *address, const void *icon, size_t icon_size);
void close_linux_config_window(void);
*/
import "C"

import (
	"context"
	"errors"
	"os"
	"runtime"
	"unsafe"
)

func init() {
	if standaloneConfigApp == "1" || (len(os.Args) > 2 && os.Args[1] == "config" && os.Args[2] == "gui") {
		runtime.LockOSThread()
	}
}

func runConfigGUI(args []string) error {
	return runDesktopWorkspace(args, func(ctx context.Context, address string) error {
		url := C.CString(address)
		defer C.free(unsafe.Pointer(url))
		cancelClose := context.AfterFunc(ctx, func() { C.close_linux_config_window() })
		defer cancelClose()
		if C.open_linux_config_window(url, unsafe.Pointer(&workspaceIcon[0]), C.size_t(len(workspaceIcon))) != 0 {
			return errors.New("unable to open the configuration window; check the Linux display session or use t3-bridge config web")
		}
		return nil
	})
}
