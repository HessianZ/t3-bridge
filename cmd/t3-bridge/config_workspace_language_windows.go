package main

import "golang.org/x/sys/windows"

func platformWorkspaceLanguage() workspaceLanguage {
	lang, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage").Call()
	if lang&0x3ff == 0x04 { // LANG_CHINESE
		return workspaceZH
	}
	return workspaceEN
}
