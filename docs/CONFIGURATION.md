# Configuration

English | [简体中文](CONFIGURATION.zh-CN.md)

T3 Bridge uses strict JSON. A selected named profile takes precedence. Without a selected profile, it uses an existing `config/local.json` in the working directory, then `t3-bridge/config.json` in the user configuration directory. Use `-profile NAME` or `-config PATH` to select a different configuration. `run -check` uses the same validation as startup and only reads the file.

If the selected file is missing, an interactive `t3-bridge run` opens the TUI setup guide. After saving, run preflight, enable receiving, and start the receiver. Non-interactive startup prints a setup hint.

## Templates

| Use case | Template |
| --- | --- |
| A new instance with one execution target | [Single route](../config/generic.example.json) |
| Multiple environments or an independent review route | [Multiple routes](../config/multi-host.example.json) |
| Signed monitoring events and maintenance coordination | [HomeOps integration](../config/homeops.example.json) |

## Configuration workspace

```sh
t3-bridge config web
t3-bridge config gui
t3-bridge config tui
```

Web prints a loopback address. Desktop builds open a native window with the same workspace. Select a saved profile or template, or pass `-config PATH` to open a specific JSON file. The setup guides you through app binding, T3 authorization, route selection, saving, and receiver preflight.

- **macOS.** Run `sh deploy/build-macos-config.sh` to build `bin/T3 Bridge Config.app` for macOS 13 or newer. The menu bar icon can reopen the workspace after the window closes. To select a profile or file, run `config gui -profile NAME` or `config gui -config PATH`.
- **Linux.** Extract the whole desktop ZIP and run `sh "./T3 Bridge Config.sh"`. The native window requires GTK 3 and WebKitGTK 4.1. On Debian or Ubuntu, source builds use `libgtk-3-dev`, `libwebkit2gtk-4.1-dev`, and `pkg-config`. Build with `CGO_ENABLED=1 go build -tags desktop -o bin/t3-bridge ./cmd/t3-bridge`.
- **Windows.** Install the WebView2 Runtime, extract the desktop ZIP, and open `T3 Bridge Config.cmd`. To build from source, run `go build -tags desktop -o t3-bridge.exe ./cmd/t3-bridge` and then `.\t3-bridge.exe config gui`. Existing private files must be owned by the runtime account. Their ACL can also include SYSTEM and local Administrators. New files grant access only to the runtime account. [WebView2 Runtime](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution)

In TUI, Tab switches accounts, arrows select a field, and Enter edits it. Press `a` for an account, `r` for a route, `p` to validate and review the diff, `b` to preview a backup, and `F4` to manage profiles. Press `F5` in a route to look up projects and models for that account and environment. After saving, `I` opens service installation. Installation and startup are separate confirmations.

Web and desktop let you edit fields or the full JSON draft. Validate and review the diff before downloading a candidate or saving to the selected file. Importing another JSON file replaces the draft while keeping the destination selected at startup. **Load backup into draft** lets you review the previous version. If the source changed, load the current file and review the diff again.

### What takes effect when

| When | Settings |
| --- | --- |
| Live | Concurrency, reply and progress display, idle timeout, user, admin, route and group ACLs, and group mention policy. A reply style or idle timeout change applies to later turns. |
| Reconnect one app | Reload that app's Feishu secret, T3 OAuth file, and T3 runtime file from the paths already loaded. You can wait for outstanding runs before reconnecting. |
| Restart the receiver | Reload JSON, including enabled apps, credential paths, MCP targets, routes, models, permissions, static ACLs, feature flags, and state directories. Existing live overrides still apply. |
| First initialization | Apply `initialRuntime` to newly bound state. Use the live controls for existing state. |

`initialRuntime.settings` can set `replyMode`, `processMode`, and `maxConcurrent`. Set `initialRuntime.requireMention` for the app default. Each key in `initialRuntime.groupMentions` must already occur in that account's `chats` list. [Receiver readiness](RUNBOOK.md#receiver-readiness)

## Named profiles

```sh
t3-bridge profile create work
t3-bridge profile create review -from work.candidate.json
t3-bridge profile list
t3-bridge profile use work
t3-bridge config web -profile work
t3-bridge run -profile review -check
t3-bridge profile export work > work.candidate.json
```

A profile is saved as `t3-bridge/profiles/NAME/config.json` in the user configuration directory. A new or cloned profile gives each app a separate state directory and starts with receiving disabled. Changing the default affects future commands. Installed services keep the configuration path selected at installation.

Stop a running profile before archiving it. `profile archive NAME` preserves its state directory and clears the default selection if needed. `profile restore ARCHIVE_PATH` restores the configuration and state association. To delete a profile permanently, preview `profile purge NAME`, then repeat with `-confirm -revision REVISION`. The preview identifies owned state, and lists custom state or external credentials that are retained.

### Private profile bundles

```sh
t3-bridge profile export work -include-secrets -yes -output "$PRIVATE_BUNDLE"
t3-bridge profile import work-copy -from "$PRIVATE_BUNDLE" -preview
t3-bridge profile import work-copy -from "$PRIVATE_BUNDLE" -expected-revision "$PREVIEW_REVISION"
```

Private export writes plaintext App Secrets to a user-only JSON file after confirmation. Keep that file in a private directory. Import creates independent App Secret files and a new state directory, with receiving disabled. T3 OAuth continues to use file references. Web and desktop offer private import and export under **Named profiles**. In TUI, use `F4`, then `e` to export or `i` to import. The preview requires confirmation.

For a different host, pass `-patch "$TARGET_EDITS"` during preview and import. It uses the same array of `path` and `value` edits as `config patch`. Adjust T3 OAuth path, environment, project, and any host-specific tool or project paths. To switch from `mcp.runtimeFile` to `mcp.endpoint`, delete the first and set the second in the same edit array. Prepare the new OAuth file on the target. For the first additional app, use paths beginning with `["additionalAccounts","0"]`. Escape Windows backslashes in JSON. Web and desktop accept the same edit file. TUI accepts it with `Ctrl+E` on the profile name screen. [Test a dedicated instance](RUNBOOK.md#test-a-dedicated-instance)

## Feishu and Lark apps

### Bind an app by QR code

1. In Web or desktop, expand **Bind a Feishu app by QR code** (`扫码绑定飞书应用`), select document comment access if needed, and generate the QR code.
2. Scan with Feishu or Lark or open the authorization page. Select or create a PersonalAgent app.
3. Confirm saving the private credential and updating the draft. The App Secret goes to `t3-bridge/credentials/APP_ID.secret` in your user configuration directory. The draft holds its path.
4. Review the App ID, T3 target, and route ACL before saving. Set a comment route if you enabled comment access.

In TUI, select the app and press `F7`. Confirm with Enter after authorization. For CLI, run `t3-bridge config bind-feishu -config PATH -output candidate.json`. Add `-comments` to request document comment access. A new App ID creates a separate account draft when a primary app already exists. It inherits the primary T3 connection and default project and model, starts with `approval-required`, grants initial app and route access to the scanning user, and keeps receiving disabled. An existing App ID updates its own secret reference. If an existing file contains a different secret, rotate it explicitly or select another private file.

### Replace an app

At the QR form, select **Replace an existing account** and choose its full App ID. In TUI select the app, then press `F7` and `x`. A replacement must use a new App ID. It receives a separate state directory, keeps the selected connection and project, and initially grants access only to the specified new user. A route with a dedicated tool binding requires selecting a new provider and model.

```sh
t3-bridge config bind-feishu -config "$CONFIG" -replace-app-id "$OLD_APP_ID" -output replacement.json
```

For manual replacement, use the private file that belongs to the new app, along with its tenant and user Open ID.

```sh
umask 077
t3-bridge config replace-app -config "$CONFIG" -app-id "$OLD_APP_ID" \
  -new-app-id "$NEW_APP_ID" -tenant feishu -app-secret-file "$NEW_SECRET_FILE" \
  -user-id "$NEW_USER_ID" > replacement.json
```

Use `-enter-secret` instead of `-app-secret-file` to enter a new secret. The terminal masks interactive input. If stdin supplies the secret, choose the saved configuration with `-config` or `-profile`. Web and desktop support both choices in **Replace an account manually**. In TUI press `F7`, `m`, and, for new secret entry, `F2`. An identical file can be reused. An existing different secret is preserved. Review the draft, drain and stop the old receiver, save, then preflight and enable the new account separately. [App replacement and rollback](RUNBOOK.md#replace-a-receiving-app)

### Multiple apps and private credentials

The primary app uses the top-level fields. Each `additionalAccounts` entry is a complete single-account configuration with its own App ID and state directory. Apps may share T3 MCP or use separate connections and routes. Each authorized app gets an independent long connection, queue, ACL, binding, and receipt store.

Use the saved configuration to list, set, rotate, or remove App Secrets. Each credential must be a regular private file with an independent reference. Check the app and the path on the confirmation screen.

```sh
t3-bridge config credentials list -profile work
t3-bridge secrets set -profile work -app-id cli_example < "$NEW_APP_SECRET_FILE"
t3-bridge secrets remove -profile work -app-id cli_example -confirm
```

Web and desktop use **App credentials**. TUI opens it with `C` and supports masked entry, import from a private file with `p`, and delete preview with `d`. An existing program can use the v1 stdin protocol with an explicit configuration selection. The result puts readable secret values in `values` and unsuccessful lookups in `errors`.

```sh
printf '%s\n' '{"protocolVersion":1,"provider":"t3-bridge","ids":["app-cli_example"]}' |
  t3-bridge secrets get -profile work -app-id cli_example
```

The receiver reads the secret when it builds the Feishu connection. Verify a rotation before reconnecting or restarting. Revocation of the actual App Secret is managed on the open platform.

## T3 targets and authorization

### First T3 MCP authorization

In Web or desktop use **Use local T3** (`采用本机 T3`) for an account with a placeholder environment. In TUI press `T`. The CLI example below writes the same draft and shows the authorization and catalog commands.

```sh
t3-bridge config local-t3 -config PATH > bridge.candidate.json
t3-bridge config authorize-t3 -config PATH
t3-bridge config catalog -config PATH -route work
```

The discovery command reads `userdata/server-runtime.json` and `environment-id` from an absolute `T3CODE_HOME` directory or `~/.t3`. Review and save the discovery draft before using its saved configuration. Set a loopback HTTP or HTTPS MCP endpoint, or a local runtime file, and give `mcp.tokenFile` an absolute private `.json` path. Use `-app-id` to select an additional app.

Open the printed address in a browser on the same host. Web and desktop offer **Request T3 authorization** (`申请 T3 授权`), and TUI offers it with `F6`. For a **Sign-in code**, use **Settings → Sharing & access → Use T3 Code in another app (MCP)** in the same T3 environment, or its `t3 auth pairing create` command. Select an appropriate permission such as **Supervised** for an `approval-required` route. Authorization waits up to five minutes. A replacement requires `-replace`. [T3 external app authorization](https://github.com/pingdotgg/t3code/blob/main/docs/user/outside-agents.md)

Catalog output shows `operateAccess` as `granted`, `missing`, or `unknown`. Read-only permission supports lookup. Enable receiving or dispatch with at least the necessary supervised access. HTTPS uses system-trusted TLS, and new HTTPS OAuth files are bound to their endpoint. Use a matching private file when changing endpoints. First-turn upload uses the same endpoint and MCP identity as dispatch. Cached follow-up files and local tool identity checks need a local receiver and T3.

### Linked T3 environments

Enable environment linking in **Settings → Labs** on both instances and link the execution instance from the connection instance. Set the account MCP endpoint and environment to the connection instance. Set the route's environment, project, provider, and model for the execution instance. Both the OAuth and the link must allow the selected runtime mode. Inspect `linkAccess` in the catalog. Read-only linking supports lookup and reuse of existing projects. Creating a remote project also needs `full-access` on the link. [T3 linked environments](https://github.com/pingdotgg/t3code/blob/v0.1.10/docs/user/remote-access.md#link-another-environment)

### Model options and project registration

Web and desktop display the selected model's boolean and string options. In TUI use `F5`, select a model with options, and edit it before saving. Selecting a default removes the field. The same binding preserves custom fields, while a new binding starts with empty options. With the CLI, validate options against the live catalog before replacing the object.

```sh
t3-bridge config model-options -config PATH -route work < options.json > candidate.json
t3-bridge config register-project -config PATH -route work -directory "$WORKSPACE" > project.candidate.json
```

Project registration uses an existing directory on the target and can reuse a unique matching project. Web and desktop support draft registration. TUI opens it with `K`. A new T3 project requires `full-access`. Use a separate authorized JSON with `-setup-token-file` if the route uses a lower-permission credential. Registration only updates project fields in the candidate. If acknowledgment is unknown, look up the directory in T3 before retrying.

Use `-managed` to create a private workspace for local T3 under `t3-bridge/workspaces`. The directory stays stable for the configuration path, App ID, and route. Provide the app identity before creating it. Use the final save path and expected revision when registering from a draft.

```sh
t3-bridge config register-project -config "$CONFIG" -route work -managed \
  -draft-stdin -expected-revision missing < "$DRAFT" > "$CANDIDATE"
t3-bridge config diff -config "$DRAFT" < "$CANDIDATE"
t3-bridge config save -config "$CONFIG" -expected-revision missing < "$CANDIDATE"
```

### Attachments on a follow-up turn

Enable `cacheFollowupAttachments` on a local route only when the T3 executor can read the account's private `stateDir/followup-attachments` directory. The Bridge queues cached paths on the original T3 thread. It can remove settled copies older than 24 hours and retains active or unreconciled copies. A same-environment first turn uses native T3 upload. If a follow-up cannot use the local cache, send `/new` and attach the files in a new message. For a linked environment, use a direct connection with upload access to that target.

| Attachment mode | Items | Per image | Per regular file | Combined limit |
| --- | ---: | ---: | ---: | --- |
| Native first turn | 8 | 10 MiB | 50 MiB | 100 MiB including at most 80 MiB of images |
| Local follow-up cache | 10 | 25 MiB | 50 MiB | 100 MiB |

Quotes, forwards, and merged inputs count toward the same turn. Native upload recognizes images by content. The Bridge rejects an over-limit turn before dispatch. [T3 attachment contract](https://github.com/pingdotgg/t3code/blob/v0.1.10/apps/server/src/mcp/toolkits/attachment/handlers.ts#L93-L110)

## Access and live settings

### Find groups and invite an app

The app's joined group list is available even while the receiver is stopped. In **Access control**, query joined groups, search by name or ID, and add selected groups to the draft. In TUI use `F8`, `/` to search, Space to select, and Enter to confirm.

```sh
t3-bridge config list-groups -profile work -app-id cli_example
```

**My groups** uses a local `lark-cli` and an authorization from the app owner. Listing and search use `im:chat:read`. Inviting the app uses `im:chat.members:write_only` and explicit confirmation. Check membership after pending approval or an unknown response. In TUI use `F10`. Open the returned URL, authorize, and use its ticket with `finish`.

```sh
t3-bridge config my-groups start -profile work -app-id cli_example -purpose list
t3-bridge config my-groups finish -profile work -app-id cli_example -ticket "$TICKET"
t3-bridge config my-groups list -profile work -app-id cli_example -query project
t3-bridge config my-groups invite -profile work -app-id cli_example -chat-id oc_example -confirm
```

Start an `invite` authorization before submitting an invite. Reuse the same query and returned `nextPageToken` for the next page. Use `status` or `cancel` to manage the authorization ticket. On Windows, the workspace can find a standalone `lark-cli.exe` or the standard npm installation. A custom npm prefix must be on the host's `PATH`.

### Live group, user, and route access

In the runtime view, choose the receiver process and app before changing live access. In TUI use `F9` for groups and `F12` for users and routes. An authorized administrator can manage the same app policies in Feishu using `/config groups`, `/invite`, and `/remove`.

```sh
t3-bridge ps
t3-bridge config live-groups -process PROCESS_ID -app-id cli_example -tenant feishu
t3-bridge config live-groups -process PROCESS_ID -app-id cli_example -tenant feishu \
  -chat-id oc_one -chat-id oc_two -access allow -confirm
t3-bridge config live-access -process PROCESS_ID -app-id cli_example -kind route \
  -user-id ou_example -route-id work -mode deny
```

Use `-tenant lark` for a Lark app. Sending a single chat ID without an operation reads its state. `joinedAvailable` indicates whether joined groups could be read. Live group settings can allow, deny, or inherit startup access and override the app's mention policy. A batch grants up to 32 joined groups. The receiver checks membership again on confirmation and rejects the whole batch if any group cannot be verified. A Web review can be used once within five minutes.

Live user, admin, and route screens show startup rules, overrides, and effective values. Revocation blocks queued dispatch and re-granting access requires a new submission. Chat scope and the remaining access checks still apply. If a live change has an unknown result, read the effective state before retrying. [Chat access commands](RUNBOOK.md#chat-commands)

### Live run settings

Use the runtime view or `F11` in TUI for concurrency, card progress, reply mode, tool activity, process display, and idle timeout.

```sh
t3-bridge config live-settings -process PROCESS_ID -app-id cli_example
t3-bridge config live-settings -process PROCESS_ID -app-id cli_example -max-concurrent 2 -progress=false
```

Concurrency ranges from 0 to 50. Zero leaves the Bridge budget unlimited. Lowering the limit preserves active runs and constrains future dispatch. Reply modes are `card`, `text`, and `markdown`. Process modes are `off`, `brief`, `detailed`, `stream`, `stream-brief`, `stream-detailed`, and `card`. Changes apply only to fields you edit. [Reply and progress modes](RUNBOOK.md#reply-and-progress-modes)

### Enable receiving per app

Save the app credentials, T3 connection, and route first. Use **Receiver preflight** (`接收预检`), or the selected account's `A` key in TUI, to check app identity, T3 access, routes, and receiver locks. Use `-include-disabled` to perform remote checks on an app with receiving disabled. `passed` reports current startup readiness. `allAccounts.passed` reports the preflight scope, including disabled accounts when requested.

```sh
t3-bridge config preflight -config "$CONFIG" -app-id "$APP_ID" -include-disabled
revision=$(t3-bridge config version -config "$CONFIG")
candidate=$(mktemp)
t3-bridge config prepare-receiver -config "$CONFIG" -app-id "$APP_ID" \
  -expected-revision "$revision" > "$candidate"
t3-bridge config diff -config "$CONFIG" < "$candidate"
```

Review and explicitly save the draft that enables `receiverAuthorized` for the selected app. TUI opens this workflow with `E`.

```sh
t3-bridge config save -config "$CONFIG" -expected-revision "$revision" < "$candidate"
rm "$candidate"
```

Service installation, starting the receiver, and sending the first test message are separate steps. [Receiver readiness](RUNBOOK.md#receiver-readiness)

## Document comments and meetings

Set `commentRoute` to enable cloud document comments. Each commenter needs app access and route access. Replies are visible in the source document. [Document comments](RUNBOOK.md#document-comments)

Set `meetingEnabled` per app to an explicit `true` and restart. It defaults to `false` and reserves `/meeting` and the corresponding route names when enabled. The app needs `vc:meeting.bot.join:write` and permission for the particular meeting. Subscribe to `vc.bot.meeting_ended_v1` to close tracked receipts when the meeting ends.

- Set `meetingRoute` to a `plan`, `approval-required` route for captions, Q&A, and summaries. Leaving it empty keeps manual join and leave available. With `vc.bot.meeting_activity_v1`, the receiver keeps the latest 30 caption lines, up to 6,000 bytes, in memory for a tracked meeting. Caption commands and T3 requests save bounded excerpts in their receipts and threads.
- Enable `meetingRoomReplies` only when the app can publish into meetings. An authorized participant can issue `@bot /ask`, `@bot /notes`, or `@bot /stop`. Replies are visible to meeting participants. This defaults to off.
- Set `meetingAutoJoinUsers` to authorized inviter Open IDs and subscribe to `vc.bot.meeting_invited_v1`. Only listed owners or admins can trigger auto-join. The default list is empty.
- Set `meetingSummaryTarget` to `origin` or `owner` to enable automatic notes after a tracked meeting ends. `origin` uses the initiating direct chat when available. Invite-based joins without a source chat use the verified authorized app owner. `owner` always uses that owner. The requester also needs the meeting route.

See the [meeting commands](RUNBOOK.md#meeting-controls) for the exact chat commands and permissions.

## Scripted configuration

### Create a draft or modify a saved file

```sh
umask 077
t3-bridge config init -draft > bridge.template.json
t3-bridge config bind-feishu -draft-stdin -output bridge.bound.json < bridge.template.json
t3-bridge config local-t3 -draft-stdin < bridge.bound.json > bridge.local.json
t3-bridge config authorize-t3 -draft-stdin < bridge.local.json
t3-bridge config catalog -draft-stdin -route work < bridge.local.json
t3-bridge config check -draft-stdin < bridge.local.json
```

For those draft commands, use `-draft-stdin` in place of `-config` or `-profile`. Authorized OAuth gets its own file. Final save, preflight, and enabling the receiver are separate steps.

```sh
t3-bridge config init -config config/local.json -template generic
revision=$(t3-bridge config version -config config/local.json)
t3-bridge config patch -config config/local.json > config/local.candidate.json <<'JSON'
[
  {"path": ["routes", "work", "model"], "value": "MODEL_ID"},
  {"path": ["routes", "work", "options"], "value": {}}
]
JSON
t3-bridge config diff -config config/local.json < config/local.candidate.json
t3-bridge config save -config config/local.json -expected-revision "$revision" < config/local.candidate.json
```

Patch reads an array of edits from stdin and validates the resulting full configuration. Use complete object keys such as `"Work.Team"`, zero-based string indices for additional accounts, and `"delete": true` for optional fields. Export and patch write to stdout. Use a different output file so the shell preserves the source. A successful save checks the source revision and keeps the previous `<filename>.backup.json`. Read it with `config backup`. Resolve a revision conflict by reopening the current file, reapplying the edit, and reviewing the diff.

### Bind a dedicated Feishu tool identity

In a route, open **Feishu tools**, or press `B` in TUI. Edit `toolContext` in the workspace or generate a candidate with `config tool-binding`.

```sh
t3-bridge config tool-binding -profile work -route work -mode bot \
  -cli-path "$LARK_CLI" -cli-config-dir "$LARK_CONFIG_DIR" \
  -cli-profile dedicated-bot -bot-open-id "$BOT_OPEN_ID" > work.candidate.json
t3-bridge config check-tool-identity -profile work -route work
```

- **Bot.** Use strict bot mode and an independently verified `botOpenId`. The local check verifies the CLI profile, bot identity, and list-group permission. Routes sharing the same provider use the same binding.
- **User.** Use `-mode user -user-open-id USER_ID` with strict user mode and the user's OAuth. The app, tenant, and only route user must match the binding. This mode accepts only that user's direct messages to the app.
- **Dual.** Use `-mode user-default` and both Open IDs. App, tenant, profile, and configuration directory must match. The dedicated profile uses `strict-mode: off` and `default-as: auto`, and the Bridge validates both identities explicitly. Use `--as user` for personal resources and `--as bot` for app actions.
- **Off.** Use `-mode off` to remove the draft binding.

Configure a dedicated local T3 provider and execution account to use the same private CLI configuration. A successful local probe establishes the CLI identity on the receiver host. A route change creates a new Bridge session context, while existing requests retain their receipts. TUI checks a saved route with `V`. In Web use the full launch address printed in the terminal so it can establish the verification session.

Authorized owners or admins can use `/config identity` or `/config identity use ROUTE` in an allowed direct chat. The selectable routes must share the environment, project, runtime mode, and interaction mode. Switching waits until outstanding work is settled. A prior route can resume its existing session if its identity and authorization still pass.

## Migration

### Import lark-channel-bridge configuration

All four entry points can import a V2 named profile or an older single-account `accounts.app` or `app` configuration. Web and desktop have an import form. TUI opens it with `L`, and `Ctrl+S` previews the result. Leave the profile blank for single-account input. V2 then uses `activeProfile` if the profile is omitted.

Discovery uses `LARK_CHANNEL_HOME/config.json` if the variable is set to an absolute directory. Otherwise it tries `~/.lark-channel/config.json` and then `$XDG_CONFIG_HOME/lark-channel-bridge/config.json` (with `~/.config` as the fallback). Use `-legacy-config` to choose a different source. The result references a private credential file, uses a new state directory, and disables receiving for every account.

```sh
t3-bridge config import-legacy \
  -state-dir "$NEW_STATE_DIR" -app-secret-file "$APP_SECRET_FILE" > bridge.candidate.json
```

Select a base with `-config` or an old profile with `-profile`. Explicit base routes keep their T3 settings and ACLs. V2 requires at least one allowed user. Review startup reply, tool display, concurrency, idle timeout, and group mention rules in the draft. Use `-reuse-t3` to reuse an old T3 connection and OAuth reference in an eligible single-route draft. Discover and verify the actual project, model, and provider before enabling. Local reuse can enable follow-up attachment caching unless the base explicitly sets it.

Use `-copy-app-secret` for a literal secret in an older single-account JSON file. For an encrypted credential store, use `-legacy-bridge "$OLD_BRIDGE_BINARY"` so the importer can request the selected profile's secret through the old `secrets get` protocol. The UI previews the app, source, and destination before copying. Confirm separately before saving the candidate. An existing different destination is preserved.

### Continue a legacy T3 session

Stop the old receiver and confirm the thread is idle. Select the same app and tenant, the original chat, requester, and an authorized route whose project path matches. Use `-list` to discover legacy chat scopes and active threads. Filter by the full `-chat-id` when known. The local catalog alone cannot establish the senders or chat type.

```sh
t3-bridge config import-session -profile work -chat-id "$CHAT_ID" -list
t3-bridge config import-session -profile work -app-id "$APP_ID" \
  -legacy-profile "$LEGACY_PROFILE" -thread-id "$THREAD_ID" \
  -chat-id "$CHAT_ID" -user-id "$USER_ID" -route work -check-t3
```

For a group, add `-chat-type group` and, if applicable, `-topic-id "$TOPIC_ID"`. Verify the app, original requester, scope, environment, project, and thread in the preview. Stop the target receiver and repeat the command with `-confirm "$REVIEW"`. In TUI use `S`, `F3` for lookups, `Ctrl+S` to preview, and `s` to confirm. `-check-t3` reads the actual idle thread and verifies dispatch access. `-prepare-route` can prepare a config draft using the original driver's model and execution settings for a compatible project. Save it through normal review, then repeat session registration separately.

The first matching input rechecks the thread and route. For a group or topic, it also verifies the original Feishu messages under the current app and requires one original sender, chat, and topic. It reads up to eight T3 input pages and verifies up to 128 Feishu messages, including merged inputs. Any mixed senders, unverified source, or overflow prevent continuation. `/new` can clear a pending registration. [Receipt lookup](RUNBOOK.md#look-up-a-message-receipt)

### Migrate named workspaces

Import the `named` object from an old `workspaces.json`. Use `-list` to get exact source keys and `-mapping` to supply a JSON array. Each entry names the source key, target user, chat, chat type, and route. Include `topicId` for topics and a specific project ID if the matching directory is ambiguous. Composite source keys use JSON `\u001f` delimiters.

```sh
t3-bridge config import-workspaces -config bridge.json -app-id cli_example -list
t3-bridge config import-workspaces -config bridge.json -app-id cli_example -mapping workspace-map.json
```

Use `-legacy-workspaces` to specify a source explicitly. The preview shows additions, retained entries, and unmapped aliases. Stop the target receiver and repeat with `-confirm "$REVIEW"`. In TUI use `W`, `F3` to select an alias, `Ctrl+S` to preview, and `s` to confirm. A conflicting alias mapped to a different project cancels the entire batch.

## Field reference

| Field | Meaning |
| --- | --- |
| `receiverMachine`, `receiverAuthorized`, `stateDir` | Instance identity, explicit startup permission, and an absolute private state directory |
| `appId`, `appSecretFile`, `tenant` | App identity and a private absolute secret path. `tenant` defaults to `feishu`, with `lark` for international apps |
| `users`, `admins`, `chats`, `topics`, `directChats` | Access scope. An empty `directChats` allows direct chats that pass user and route checks. Omitting it uses the legacy `chats` boundary for direct chats |
| `mcp.endpoint` or `mcp.runtimeFile` | A loopback HTTP or HTTPS `/mcp` endpoint, or the absolute local `server-runtime.json` path. Use exactly one |
| `mcp.environmentId`, `mcp.tokenFile` | Environment returned by the endpoint and a private raw token file or OAuth JSON containing `access_token` |
| `routes` | Route IDs map to an environment, project, driver, provider, model, options, runtime mode, interaction mode, and allowed users. Optional `hostName` and `aliases` must be unique and avoid reserved commands |
| `defaultRoute`, `reviewRoute`, `commentRoute` | Default chat route, dedicated `/review` route, and optional document comment route |
| `classifier` | Optional verified official Hy4 endpoint and private key file. Authorized chat can use the default route without a classifier |

Validation rejects unknown fields, incorrectly cased fixed fields, duplicate keys, invalid types, and trailing documents. Dynamic route IDs, check names, and model option keys preserve case and dots. The JSON limit is 1 MiB. Runtime settings and dynamic ACLs are stored separately from configuration.
