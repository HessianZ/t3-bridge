# Lark Channel Go SDK

Production sources from [v0.1.0](https://github.com/larksuite/channel-sdk-go/tree/v0.1.0), commit `e08f61fe33a03f78ba619f77bf2c58b2e3e260fa`. The upstream MIT license is in this directory.

Local changes distinguish replies under the same document comment in the SDK's duplicate cache and processing lock, register typed meeting bot activity and end events on the existing dispatcher, and decode incoming posts whose `content` or `content_v2` is at the top level. Replace this snapshot with the upstream module once these behaviors are available.
