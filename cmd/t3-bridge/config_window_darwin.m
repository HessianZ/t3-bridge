//go:build darwin && cgo

#import <AppKit/AppKit.h>
#import <WebKit/WebKit.h>

int workspace_uses_chinese(void) {
    @autoreleasepool {
        for (NSString *preferred in [NSLocale preferredLanguages]) {
            NSString *language = [[preferred componentsSeparatedByString:@"-"] firstObject];
            if ([language isEqualToString:@"zh"]) return 1;
            if ([language isEqualToString:@"en"]) return 0;
        }
        return 0;
    }
}

static void stop_application(void) {
    [NSApp stop:nil];
    NSEvent *wake = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
        location:NSZeroPoint modifierFlags:0 timestamp:0 windowNumber:0 context:nil
        subtype:0 data1:0 data2:0];
    [NSApp postEvent:wake atStart:YES];
}

@interface ConfigWindow : NSObject <NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate, WKDownloadDelegate>
@property(nonatomic, retain) NSURL *origin;
@property(nonatomic, assign) NSWindow *window;
@property(nonatomic, retain) NSStatusItem *statusItem;
@end

@implementation ConfigWindow

- (void)dealloc {
    [_statusItem release];
    [_origin release];
    [super dealloc];
}

- (void)showWorkspace:(id)sender {
    [NSApp activateIgnoringOtherApps:YES];
    if (self.window.miniaturized) {
        [self.window deminiaturize:nil];
    }
    [self.window makeKeyAndOrderFront:nil];
}

- (BOOL)applicationShouldHandleReopen:(NSApplication *)application hasVisibleWindows:(BOOL)visible {
    if (!visible && self.statusItem) {
        [self showWorkspace:nil];
        return NO;
    }
    return YES;
}

- (BOOL)windowShouldClose:(NSWindow *)window {
    if (!self.statusItem) {
        return YES;
    }
    [window orderOut:nil];
    return NO;
}

- (void)windowWillClose:(NSNotification *)notification {
    stop_application();
}

- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)application {
    stop_application();
    return NSTerminateCancel;
}

- (BOOL)isLocal:(NSURL *)url {
    return [url.scheme isEqualToString:self.origin.scheme] &&
        [url.host isEqualToString:self.origin.host] &&
        [url.port isEqualToNumber:self.origin.port];
}

- (BOOL)isBrowserLink:(NSURL *)url {
    if (url.user || url.password) {
        return NO;
    }
    if ([url.scheme isEqualToString:@"https"]) {
        return YES;
    }
    return [url.scheme isEqualToString:@"http"] &&
        [url.path isEqualToString:@"/oauth/mcp/authorize"] &&
        [@[@"localhost", @"127.0.0.1", @"::1", @"[::1]"] containsObject:url.host ?: @""];
}

- (void)webView:(WKWebView *)view decidePolicyForNavigationAction:(WKNavigationAction *)action
    decisionHandler:(void (^)(WKNavigationActionPolicy))decide {
    NSURL *url = action.request.URL;
    if ([self isLocal:url]) {
        decide(WKNavigationActionPolicyAllow);
    } else {
        decide(WKNavigationActionPolicyCancel);
        if (action.navigationType == WKNavigationTypeLinkActivated && [self isBrowserLink:url]) {
            [[NSWorkspace sharedWorkspace] openURL:url];
        }
    }
}

- (WKWebView *)webView:(WKWebView *)view createWebViewWithConfiguration:(WKWebViewConfiguration *)config
    forNavigationAction:(WKNavigationAction *)action windowFeatures:(WKWindowFeatures *)features {
    NSURL *url = action.request.URL;
    if ([self isLocal:url]) {
        [view loadRequest:action.request];
    } else if (action.navigationType == WKNavigationTypeLinkActivated && [self isBrowserLink:url]) {
        [[NSWorkspace sharedWorkspace] openURL:url];
    }
    return nil;
}

- (void)webView:(WKWebView *)view runOpenPanelWithParameters:(WKOpenPanelParameters *)parameters
    initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(NSArray<NSURL *> *))complete {
    if (!frame.mainFrame || ![self isLocal:frame.request.URL]) {
        complete(nil);
        return;
    }
    NSOpenPanel *panel = [NSOpenPanel openPanel];
    panel.canChooseDirectories = NO;
    panel.allowsMultipleSelection = NO;
    [panel beginSheetModalForWindow:view.window completionHandler:^(NSModalResponse result) {
        complete(result == NSModalResponseOK ? panel.URLs : nil);
    }];
}

- (void)webView:(WKWebView *)view decidePolicyForNavigationResponse:(WKNavigationResponse *)response
    decisionHandler:(void (^)(WKNavigationResponsePolicy))decide {
    if (![self isLocal:response.response.URL]) {
        decide(WKNavigationResponsePolicyCancel);
        return;
    }
    NSHTTPURLResponse *http = (NSHTTPURLResponse *)response.response;
    NSString *disposition = [http valueForHTTPHeaderField:@"Content-Disposition"];
    decide([disposition hasPrefix:@"attachment"] ? WKNavigationResponsePolicyDownload : WKNavigationResponsePolicyAllow);
}

- (void)webView:(WKWebView *)view navigationResponse:(WKNavigationResponse *)response didBecomeDownload:(WKDownload *)download {
    download.delegate = self;
}

- (void)download:(WKDownload *)download decideDestinationUsingResponse:(NSURLResponse *)response
    suggestedFilename:(NSString *)filename completionHandler:(void (^)(NSURL *))complete {
    NSSavePanel *panel = [NSSavePanel savePanel];
    panel.nameFieldStringValue = filename.lastPathComponent;
    [panel beginSheetModalForWindow:NSApp.keyWindow completionHandler:^(NSModalResponse result) {
        complete(result == NSModalResponseOK ? panel.URL : nil);
    }];
}

@end

void open_config_window(const char *address, int menu_bar) {
    @autoreleasepool {
        NSApplication *app = [NSApplication sharedApplication];
        [app setActivationPolicy:NSApplicationActivationPolicyRegular];
        ConfigWindow *delegate = [[ConfigWindow alloc] init];
        delegate.origin = [NSURL URLWithString:[NSString stringWithUTF8String:address]];
        app.delegate = delegate;

        NSMenu *menu = [[[NSMenu alloc] init] autorelease];
        NSMenuItem *item = [[[NSMenuItem alloc] init] autorelease];
        [menu addItem:item];
        item.submenu = [[[NSMenu alloc] initWithTitle:@"t3-bridge"] autorelease];
        [item.submenu addItemWithTitle:@"退出 t3-bridge" action:@selector(terminate:) keyEquivalent:@"q"];

        NSMenuItem *file = [[[NSMenuItem alloc] initWithTitle:@"文件" action:NULL keyEquivalent:@""] autorelease];
        file.submenu = [[[NSMenu alloc] initWithTitle:@"文件"] autorelease];
        [file.submenu addItemWithTitle:@"关闭窗口" action:@selector(performClose:) keyEquivalent:@"w"];
        [menu addItem:file];

        NSMenuItem *edit = [[[NSMenuItem alloc] initWithTitle:@"编辑" action:NULL keyEquivalent:@""] autorelease];
        edit.submenu = [[[NSMenu alloc] initWithTitle:@"编辑"] autorelease];
        [edit.submenu addItemWithTitle:@"撤销" action:@selector(undo:) keyEquivalent:@"z"];
        NSMenuItem *redo = [edit.submenu addItemWithTitle:@"重做" action:@selector(redo:) keyEquivalent:@"z"];
        redo.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagShift;
        [edit.submenu addItem:[NSMenuItem separatorItem]];
        [edit.submenu addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
        [edit.submenu addItemWithTitle:@"拷贝" action:@selector(copy:) keyEquivalent:@"c"];
        [edit.submenu addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
        [edit.submenu addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];
        [menu addItem:edit];
        app.mainMenu = menu;

        NSSize minimumSize = NSMakeSize(500, 410);
        NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
            NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable | NSWindowStyleMaskFullSizeContentView;
        NSRect content = NSMakeRect(0, 0, minimumSize.width, minimumSize.height);
        NSWindow *window = [[NSWindow alloc] initWithContentRect:content styleMask:style
            backing:NSBackingStoreBuffered defer:NO];
        window.releasedWhenClosed = NO;
        window.title = @"T3 Bridge";
        window.titleVisibility = NSWindowTitleHidden;
        window.titlebarAppearsTransparent = YES;
        window.titlebarSeparatorStyle = NSTitlebarSeparatorStyleNone;
        window.movableByWindowBackground = YES;
        window.minSize = minimumSize;
        window.delegate = delegate;
        delegate.window = window;

        if (menu_bar) {
            delegate.statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
            NSStatusBarButton *button = delegate.statusItem.button;
            NSImage *image = [NSImage imageWithSystemSymbolName:@"link" accessibilityDescription:@"T3 Bridge"];
            if (image) {
                image.template = YES;
                button.image = image;
            } else {
                button.title = @"T3";
            }
            button.toolTip = @"T3 Bridge 配置工作台";

            NSMenu *statusMenu = [[[NSMenu alloc] init] autorelease];
            NSMenuItem *show = [statusMenu addItemWithTitle:@"打开配置工作台" action:@selector(showWorkspace:) keyEquivalent:@""];
            show.target = delegate;
            [statusMenu addItem:[NSMenuItem separatorItem]];
            NSMenuItem *quit = [statusMenu addItemWithTitle:@"退出配置工作台" action:@selector(terminate:) keyEquivalent:@""];
            quit.target = app;
            delegate.statusItem.menu = statusMenu;
        }

        WKWebViewConfiguration *config = [[[WKWebViewConfiguration alloc] init] autorelease];
        config.websiteDataStore = [WKWebsiteDataStore nonPersistentDataStore];
        [config.userContentController addUserScript:[[[WKUserScript alloc]
            initWithSource:@"document.documentElement.classList.add('mac-window')"
            injectionTime:WKUserScriptInjectionTimeAtDocumentEnd forMainFrameOnly:YES] autorelease]];
        WKWebView *view = [[WKWebView alloc] initWithFrame:window.contentView.bounds configuration:config];
        view.navigationDelegate = delegate;
        view.UIDelegate = delegate;
        window.contentView = view;
        [view loadRequest:[NSURLRequest requestWithURL:delegate.origin]];
        [window center];
        [delegate showWorkspace:nil];
        [app run];
        if (delegate.statusItem) {
            [[NSStatusBar systemStatusBar] removeStatusItem:delegate.statusItem];
        }
        [view stopLoading];
        view.navigationDelegate = nil;
        view.UIDelegate = nil;
        window.delegate = nil;
        [window close];
        app.delegate = nil;
        [view release];
        [window release];
        [delegate release];
    }
}

void close_config_window(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        stop_application();
    });
}
