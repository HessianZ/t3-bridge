# go-webview2 v1.0.23

This snapshot comes from the cached Go module `github.com/wailsapp/go-webview2@v1.0.23`. It contains `pkg/webview2`, `pkg/combridge` and `webviewloader`, including the loader's embedded assets. The upstream module and loader licenses are preserved.

The generated AddScriptToExecuteOnDocumentCreated, ExecuteScript and CallDevToolsProtocolMethod completion handlers register callbacks with a Go `string` parameter. Windows passes an `LPCWSTR`, and Go panics during package initialization because a `string` exceeds one ABI word. The change in `pkg/webview2/com.go` registers those three signatures with a `*uint16` parameter, then decodes it as UTF-16 before calling the original handler. The generated files are copied verbatim.
