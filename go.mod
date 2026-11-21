module github.com/HessianZ/t3-bridge

go 1.25.0

require (
	github.com/charmbracelet/bubbles v0.21.1
	github.com/charmbracelet/bubbletea v1.3.10
	github.com/charmbracelet/x/ansi v0.11.5
	github.com/charmbracelet/x/term v0.2.2
	github.com/gorilla/websocket v1.5.0
	github.com/larksuite/channel-sdk-go v0.1.0
	github.com/larksuite/oapi-sdk-go/v3 v3.11.0
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/wailsapp/go-webview2 v1.0.23
	golang.org/x/sys v0.41.0
)

require (
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/charmbracelet/colorprofile v0.4.1 // indirect
	github.com/charmbracelet/lipgloss v1.1.0 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/clipperhouse/displaywidth v0.9.0 // indirect
	github.com/clipperhouse/stringish v0.1.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.5.0 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/jchv/go-winloader v0.0.0-20210711035445-715c2860da7e // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.3.8 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/larksuite/channel-sdk-go => ./third_party/channel-sdk-go

replace github.com/wailsapp/go-webview2 v1.0.23 => ./third_party/go-webview2
