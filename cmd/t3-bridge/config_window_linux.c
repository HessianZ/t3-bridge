//go:build linux && desktop && cgo

#include <gtk/gtk.h>
#include <libsoup/soup.h>
#include <webkit2/webkit2.h>

static GtkWidget *workspace_window;

static gboolean same_origin(GUri *address, GUri *origin) {
    return address && g_strcmp0(g_uri_get_scheme(address), g_uri_get_scheme(origin)) == 0 &&
           g_strcmp0(g_uri_get_host(address), g_uri_get_host(origin)) == 0 &&
           g_uri_get_port(address) == g_uri_get_port(origin) && !g_uri_get_userinfo(address);
}

static gboolean browser_link(GUri *address) {
    if (!address || g_uri_get_userinfo(address)) {
        return FALSE;
    }
    const char *scheme = g_uri_get_scheme(address);
    if (g_strcmp0(scheme, "https") == 0) {
        return TRUE;
    }
    const char *host = g_uri_get_host(address);
    return g_strcmp0(scheme, "http") == 0 &&
           g_strcmp0(g_uri_get_path(address), "/oauth/mcp/authorize") == 0 &&
           (g_strcmp0(host, "localhost") == 0 || g_strcmp0(host, "127.0.0.1") == 0 || g_strcmp0(host, "::1") == 0);
}

static gboolean decide_policy(WebKitWebView *view, WebKitPolicyDecision *decision,
                              WebKitPolicyDecisionType type, gpointer origin) {
    if (type == WEBKIT_POLICY_DECISION_TYPE_RESPONSE) {
        WebKitURIResponse *response = webkit_response_policy_decision_get_response(WEBKIT_RESPONSE_POLICY_DECISION(decision));
        const char *uri = webkit_uri_response_get_uri(response);
        g_autoptr(GUri) address = uri ? g_uri_parse(uri, G_URI_FLAGS_NONE, NULL) : NULL;
        if (!same_origin(address, origin)) {
            webkit_policy_decision_ignore(decision);
            return TRUE;
        }
        SoupMessageHeaders *headers = webkit_uri_response_get_http_headers(response);
        const char *disposition = headers ? soup_message_headers_get_one(headers, "Content-Disposition") : NULL;
        if (disposition && g_str_has_prefix(disposition, "attachment")) {
            webkit_policy_decision_download(decision);
            return TRUE;
        }
        return FALSE;
    }

    WebKitNavigationAction *action = webkit_navigation_policy_decision_get_navigation_action(WEBKIT_NAVIGATION_POLICY_DECISION(decision));
    WebKitURIRequest *request = webkit_navigation_action_get_request(action);
    const char *uri = webkit_uri_request_get_uri(request);
    g_autoptr(GUri) address = uri ? g_uri_parse(uri, G_URI_FLAGS_NONE, NULL) : NULL;
    if (same_origin(address, origin)) {
        if (type != WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) {
            return FALSE;
        }
        webkit_web_view_load_request(view, request);
    } else if (browser_link(address) && webkit_navigation_action_is_user_gesture(action) &&
               webkit_navigation_action_get_navigation_type(action) == WEBKIT_NAVIGATION_TYPE_LINK_CLICKED) {
        gtk_show_uri_on_window(GTK_WINDOW(workspace_window), uri, GDK_CURRENT_TIME, NULL);
    }
    webkit_policy_decision_ignore(decision);
    return TRUE;
}

static gboolean download_destination(WebKitDownload *download, const char *suggested, gpointer unused) {
    GtkFileChooserNative *dialog = gtk_file_chooser_native_new("保存候选配置", GTK_WINDOW(workspace_window),
        GTK_FILE_CHOOSER_ACTION_SAVE, "保存", "取消");
    GtkFileChooser *chooser = GTK_FILE_CHOOSER(dialog);
    gtk_file_chooser_set_do_overwrite_confirmation(chooser, TRUE);
    gtk_file_chooser_set_current_name(chooser, suggested);
    if (gtk_native_dialog_run(GTK_NATIVE_DIALOG(dialog)) == GTK_RESPONSE_ACCEPT) {
        g_autofree char *uri = gtk_file_chooser_get_uri(chooser);
        webkit_download_set_allow_overwrite(download, TRUE);
        webkit_download_set_destination(download, uri);
    } else {
        webkit_download_cancel(download);
    }
    g_object_unref(dialog);
    return TRUE;
}

static void download_started(WebKitWebContext *context, WebKitDownload *download, gpointer unused) {
    g_signal_connect(download, "decide-destination", G_CALLBACK(download_destination), NULL);
}

static void title_changed(WebKitWebView *view, GParamSpec *property, GtkWindow *window) {
    const char *title = webkit_web_view_get_title(view);
    if (title && *title) {
        gtk_window_set_title(window, title);
    }
}

static void window_destroyed(GtkWidget *window, gpointer unused) {
    workspace_window = NULL;
    gtk_main_quit();
}

static gboolean dismiss_window(gpointer unused) {
    if (workspace_window) {
        gtk_widget_destroy(workspace_window);
    }
    return G_SOURCE_REMOVE;
}

int open_linux_config_window(const char *address, const void *icon, size_t icon_size) {
    g_autoptr(GUri) origin = g_uri_parse(address, G_URI_FLAGS_NONE, NULL);
    if (!origin || !gtk_init_check(NULL, NULL)) {
        return 1;
    }

    workspace_window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
    {
        g_autoptr(GInputStream) stream = g_memory_input_stream_new_from_data(icon, icon_size, NULL);
        g_autoptr(GdkPixbuf) pixbuf = gdk_pixbuf_new_from_stream(stream, NULL, NULL);
        if (pixbuf) {
            gtk_window_set_icon(GTK_WINDOW(workspace_window), pixbuf);
        }
    }
    gtk_window_set_title(GTK_WINDOW(workspace_window), "T3 Bridge 配置工作台");
    gtk_window_set_default_size(GTK_WINDOW(workspace_window), 1040, 800);
    gtk_widget_set_size_request(workspace_window, 620, 480);
    g_signal_connect(workspace_window, "destroy", G_CALLBACK(window_destroyed), NULL);

    WebKitWebContext *context = webkit_web_context_new_ephemeral();
    g_signal_connect(context, "download-started", G_CALLBACK(download_started), NULL);
    GtkWidget *view = webkit_web_view_new_with_context(context);
    g_object_unref(context);
    g_signal_connect(view, "decide-policy", G_CALLBACK(decide_policy), origin);
    g_signal_connect(view, "notify::title", G_CALLBACK(title_changed), workspace_window);
    gtk_container_add(GTK_CONTAINER(workspace_window), view);
    webkit_web_view_load_uri(WEBKIT_WEB_VIEW(view), address);
    gtk_widget_show_all(workspace_window);
    gtk_main();
    return 0;
}

void close_linux_config_window(void) {
    g_idle_add(dismiss_window, NULL);
}
