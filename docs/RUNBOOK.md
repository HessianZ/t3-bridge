# Runbook

English | [简体中文](RUNBOOK.zh-CN.md)

## Build release packages

Build in the repository with Go 1.25 or newer and `zip`. The script embeds the supplied version and prints the ZIP path and SHA-256. After extraction, run the bundled binary with `--version`.

```sh
sh deploy/build-release.sh v0.1.0 bin/t3-bridge-mac.zip desktop
GOOS=linux GOARCH=amd64 sh deploy/build-release.sh v0.1.0 bin/t3-bridge-linux-amd64.zip core
GOOS=windows GOARCH=amd64 sh deploy/build-release.sh v0.1.0 bin/t3-bridge-windows-amd64.zip desktop
```

The first example runs on macOS. Core builds provide CLI, TUI, and Web. Desktop builds also provide `config gui`. Linux desktop builds require a Linux host with GTK 3, WebKitGTK 4.1 development files, and `pkg-config`. On Linux, run `sh "./T3 Bridge Config.sh"` from the extracted desktop package. On Windows, install the WebView2 Runtime and open `T3 Bridge Config.cmd`. Extract the whole package so its files remain together.

Build the standalone macOS configuration app with the dedicated script.

```sh
T3_BRIDGE_BUILD_VERSION=v0.1.0 sh deploy/build-macos-config.sh 'bin/T3 Bridge Config.app'
```

The macOS desktop ZIP and app target macOS 13. The app build uses an ad hoc signature. Public distribution of the app requires separate Developer ID signing and notarization. ZIP packages and the app's `Contents/Resources` include the license and third-party notices. ZIPs also include the English and Chinese guides. [License](../LICENSE) · [Third-party notices](../THIRD_PARTY_NOTICES.md)

## Start and deploy

1. Configure a dedicated T3 MCP identity and verify each route's environment, project, provider, and model. Prepare a Feishu or Lark app with message receipt and reply permissions and long-connection delivery.
2. Store credentials in private files readable by the receiver account. Validate JSON, verify the app and T3 target, then review and save the draft that enables receiving. [Enable receiving per app](CONFIGURATION.md#enable-receiving-per-app)
3. Start a dedicated receiver first. Verify direct messages, group access, continuation, and stopping. `/doctor` reads the latest known checks. `/doctor --echo ROUTE_ID` tests an authorized supervised plan route.
4. On Linux, the [example systemd unit](../deploy/t3-bridge.service) uses a dedicated `t3-bridge` account, `/etc/t3-bridge/config.json`, and `/var/lib/t3-bridge`. Check the actual paths and account permissions.

Run one receiver for each app. Different instances need separate apps, credentials, and state directories. The receiver locks per tenant and App ID under the user configuration directory. For a custom systemd service, give its account a writable configuration directory. The example sets `XDG_CONFIG_HOME` in the service state directory.

When replacing an instance, pause input, drain work and notifications, stop the old receiver, and verify it exited before starting the new one. Preserve state, receipts, and the previous program for rollback. For a macOS LaunchAgent cutover, disable the old login item and wait for the old job to unload. A local `mcp.runtimeFile` lets each connection discover the current T3 desktop port.

### User-level background services

`service` manages macOS LaunchAgents, systemd user services, and Windows Task Scheduler entries by instance name. `render` prints a definition, `install` writes it, and `start` starts the receiver and enables login startup.

```sh
t3-bridge service render -instance work -config /private/bridge/work.json
t3-bridge service install -instance work -config /private/bridge/work.json
t3-bridge service start -instance work
t3-bridge service status -instance work
t3-bridge service list
t3-bridge service logs -instance work -lines 100
```

Use `-profile NAME` in place of `-config PATH` for render and install. `restart` restarts the named instance. `stop` stops it and disables login startup. `unregister` removes its service definition while retaining configuration, credentials, state, and logs. A shared process stops all its app receivers. Already submitted T3 work can continue.

The Web and desktop runtime view lists the current user's managed services and receiver processes. Open service installation after selecting a saved file. Verify the executable path, configuration path, and instance name. Install and start separately. Put a standalone macOS app in its intended permanent location first. In TUI, `I` opens installation and `M` opens managed services. Use `l` for logs, `s` for start, `x` for stop, `R` for restart, and `u` for unregister. Confirm actions as prompted.

Before installing or enabling receiving, use `t3-bridge config preflight -profile work` or select a saved configuration in the workspace. Add `-include-disabled` to verify disabled apps and `-app-id APP_ID` to limit the check. Verify the actual long connection and first message after start. [Enable receiving per app](CONFIGURATION.md#enable-receiving-per-app)

Windows uses the current interactive user's normal permissions. Service logs are under `%LOCALAPPDATA%\t3-bridge-service-logs\INSTANCE\`. The supervisor retries a nonzero receiver exit once per minute, at most three times per start. A normal exit ends retrying. `service stop` disables the task and ends its supervised processes, including pending retries. To replace an older task with the supervisor-backed definition, drain, stop, unregister, and reinstall with the same instance name and configuration. Use `ps` to distinguish a receiver process from Task Scheduler waiting to retry.

### Receiver readiness

Use the full process ID from `ps` to read the live receiver sample for one app.

```sh
t3-bridge config receiver-status -process PROCESS_ID -app-id APP_ID -tenant feishu
```

Use `-tenant lark` for Lark. Samples include observation time, diagnostics, and recent events. An absent or stale sample is unknown. The workspace can display connection, tasks, reconciliation, delivery gaps, and capacity. TUI uses `Ctrl+D` for the selected app. Shared instances can show a worker as `retrying` or `exhausted` even when no current sample exists.

Use `receiver-readiness` to verify all apps in a saved file.

```sh
t3-bridge config receiver-readiness -config "$CONFIG"
t3-bridge config receiver-readiness -config "$CONFIG" -wait 30s
```

You can select a profile or pin `-expected-revision`. A ready result requires each enabled app to be owned by exactly one process using the actual SHA-256 revision, with a recent valid T3 environment check and a ready Feishu WebSocket. Reconnecting, stale, disabled-only, or older receivers without a version can be inspected but do not satisfy readiness. `-wait` exits zero only for readiness. On timeout it returns the last snapshot and a nonzero code. If the configuration revision changes while waiting, it exits with an error. In TUI, use `Ctrl+P` for whole-configuration readiness.

### Receiver processes and reconnects

Use `t3-bridge ps` for foreground and managed receiver processes. `t3-bridge kill PROCESS_ID` requests a normal exit. Use `service stop` to stop a managed service and disable auto-start. `--version`, `ps`, and the runtime view help compare executable versions.

Reconnect an individual app from its runtime entry, with `Ctrl+R` in TUI, or with the CLI.

```sh
t3-bridge config live-reconnect -process PROCESS_ID -app-id APP_ID -tenant feishu -confirm
```

Use `-action wait` to pause new dispatch for that app and reconnect after its T3 work settles, or `-action cancel` to cancel a pending wait. Incoming events and original receipts remain tracked. A request acknowledged as `started` or `recovery-requested` still needs a fresh sample to confirm the new WebSocket connection. Read back state before retrying an `unconfirmed` result. A shared receiver retries a failed app every 30 seconds up to three attempts in a round. Immediate reconnect can recover a worker after you fix its credentials or connection.

### Logs and telemetry

Use the workspace or `service logs` to read service output. macOS and Windows read stdout and stderr. Linux reads the journal for the current user's managed unit. The workspace shows up to 100 lines per stream. With `-telemetry`, the CLI returns allowlisted JSONL records, including per-app metrics, diagnostics, failures, and receiver events. `run_e2e_ms` covers submission through verified T3 completion, including queueing, approval, and delegated work. `ws_reconnect` records a successful Feishu reconnect. `pool_active` and `pool_waiting` track the current receiver pool.

```sh
t3-bridge service logs -instance work -lines 200 -telemetry
t3-bridge config telemetry-archive -profile work -app-id cli_example -date YYYY-MM-DD -lines 100
t3-bridge service logs -instance work -telemetry -follow -lines 0
```

Filter streamed service telemetry with `-tenant` and `-app-id`. The per-app archive lives under the state directory in `telemetry/bridge-YYYY-MM-DD.jsonl` and can be read with the receiver stopped. It uses UTC dates. `LARK_CHANNEL_LOG_DAYS` sets archive retention from 1 to 3,650 days, with 30 days as the default. Platform logs have their own retention. Live following is best-effort across rapid rotation and reads up to 128 KiB at a time on macOS and Windows.

### Connect a legacy telemetry adapter

Optional release scripts can forward local telemetry to an existing Node 20.12 or newer adapter. `--module` accepts a local module or an installed package. `LARK_CHANNEL_TELEMETRY_MODULE` supplies the default.

```sh
t3-bridge service logs -instance work -telemetry -follow -lines 0 |
  node deploy/lark-telemetry-relay.mjs --bridge-version "$(t3-bridge --version)" \
    --module "/absolute/path/to/bridge-monitor.mjs"
```

The module can export a default adapter or `createAdapter` and receives app, tenant, hostname, and version. It decides where to send telemetry. The relay maps receiver events to `run.started`, `run.completed`, `run.failed`, `outbound.sent`, and `ws.reconnected`. Events reflect recorded Bridge observations and can be absent after an interruption. Periodic tool usage and cost are outside this input. Shutdown attempts `flush` and `close` within the shared two-second budget.

### Drain before switching

Pause new input and check original T3 runs, approvals, delegated work, and Feishu delivery. Let the original receiver reconcile stopped and completed work. After stopping it, run `t3-bridge config drain -config PATH` or `config drain -profile NAME` to check all saved apps, including disabled accounts. TUI opens the selected file's check with `G`. Web and desktop can open receipts for listed samples, and TUI uses `v` on the selected app. A zero exit status means all accounts are drained.

Unknown state, a held lock, missing ownership evidence, outstanding work, or incomplete notification must be investigated before starting a replacement. Keep the old receiver stopped during the cutover. `-drain-check -app-id APP_ID` remains available for existing per-app scripts.

### Migrate or upgrade on macOS

For lark-channel-bridge, import into a candidate with an independent state directory, private credentials, and receiving disabled. Check the selected routes, run preflight, and render the new LaunchAgent. Stop and disable the old receiver after checking its work and delivery. Register an existing thread before startup if you need continuation. Enable the candidate, start it, and verify a new message and its receipt. [Import and continuation](CONFIGURATION.md#migration)

For an existing T3 Bridge receiver, drain and stop it, then back up the executable, service definition, configuration, and state. Upgrade using the same state directory for the same app. Read readiness for the new revision, then test fresh messages, continuation, and stop. Before rolling back, drain again and check that the old executable reads the new configuration and state. If new receipts are incompatible with the older version, reconcile them against the original T3 runs and Feishu messages.

### Replace a receiving app

Use `/account` to identify the receiving app and `/account change` for instructions. For a different App ID, prepare a replacement draft and verify the new app with its independent state directory and private secret. Drain and stop the old receiver before saving. After preflight, explicitly enable and start the replacement. Verify `/account` in its direct chat. Roll back by draining and stopping the new receiver and restoring the old configuration and state. [Replace an app](CONFIGURATION.md#replace-an-app)

## Document comments

Use an app with Drive comment read and reply access and subscribe to `drive.notice.comment_add_v1` over long connections. Grant it access to the test document. Set `commentRoute` and allow the commenter at both app and route scope.

Mention the bot in an inline comment or its reply thread. Replies stay in the original thread. Whole-document mentions get a new whole-document comment. Supported file types include `doc`, `docx`, `sheet`, and `file`, including accessible Wiki nodes resolved to those files. Selection context and previous comments are bounded, and accessible new documents can include up to the first 6,000 characters. Each authorized commenter gets a separate T3 session.

`/doc` reports availability. An owner or admin can use `/doc tasks [PAGE]` from an authorized direct chat to look up tokens. Use `/stop comment:TOKEN` to stop the original run and `/timeout comment:TOKEN [MINUTES|off|default]` for a 1–120 minute idle timeout override on that thread. Reopen the list if the turn changes. The bot can show a typing reaction until completion. If final comment delivery is uncertain, the receipt is preserved for reconciliation.

## Test a dedicated instance

Use a separate Feishu app, at least two test users, a test group, a dedicated T3 project, and an empty state directory. Record only the redacted app, environment, project, provider, model, and receipt details needed to verify results.

1. Send a unique marker in a direct chat. Compare the Feishu card with the T3 thread and run. Send a follow-up and verify the same thread starts a new turn. Read both results with `/result`.
2. Verify group permissions and mention rules. Use an unauthorized user and a route the allowed user cannot access. Confirm neither rejected message starts a T3 run.
3. Stop an active test run using `/stop` and check the original Feishu message and T3 run. A later message should run independently.
4. Restart the receiver during a different run. Verify recovery uses the same thread, run, and message receipt and does not duplicate submission or delivery.
5. Test a controlled stop and rollback. Check receipts, deduplication state, and `config drain` afterward.
6. If migrating a legacy session, create it first with the dedicated old app, stop it, register that thread offline, and send a new input from the same authorized chat. Verify the new run uses the same thread. Test that a different scope cannot inherit it. [Continue a legacy session](CONFIGURATION.md#continue-a-legacy-t3-session)
7. For dual tool identity, verify the dedicated provider reads app-visible and user-visible test resources under the intended identities. Test refusal for another user, a group, and a continuation after revocation. [Tool identities](CONFIGURATION.md#bind-a-dedicated-feishu-tool-identity)

### Look up a message receipt

```sh
t3-bridge config receipt -profile work -app-id "$APP_ID" -message-id "$FEISHU_MESSAGE_ID"
```

Use the original inbound Feishu ID. TUI uses `v` for the selected app. Web and desktop offer **Message receipt** for a saved configuration. The snapshot can show a queued input, a current or historical turn, or the representative `inputId` of a merged input. Compare environment, project, thread, run, request, and reply IDs before and after restart.

`deliveryState` can be `pending`, `confirmed`, `unconfirmed`, `unknown`, or `not_applicable` for deliveries with independent receipts. Confirmation records the Feishu API response. Check client visibility in Feishu. A lost card-creation receipt can be recovered by its original UUID. Until final update is confirmed, follow-ups in the same session wait. Use `/new` for an independent session.

When T3 returns a verified HTTPS `threadUrl`, Web and desktop can open it and TUI and CLI display it. Access remains subject to T3 authorization. Older receipts still carry thread and run IDs.

## Chat commands

See the [Feishu slash command reference](FEISHU-COMMANDS.md) for complete syntax, aliases, and permissions.

| Command | Behavior |
| --- | --- |
| `/help`, `/status` | Commands, current route, session, and outstanding work |
| `/status tasks [CHAT_ID[:TOPIC_ID]] [PAGE]` | Owner or admin lists current app's chat turns from an authorized direct chat |
| `/doc`, `/doc tasks [PAGE]` | Document comment availability and authorized task lookup |
| `/ps`, `/account`, `/account change` | Receiver status, receiving app identity, and app replacement instructions |
| `/exit INSTANCE_ID` | Owner or admin can stop a single-app instance from an authorized direct chat. Use host controls for a shared instance |
| `/new`, `/reset` | Clear the current mapping and request stop for active work |
| `/new chat [NAME]` | Create a private group within the current project context |
| `/resume`, `/resume page PAGE`, `/resume use TOKEN` | Browse and resume the user's sessions in this scope |
| `/resume --stop TOKEN` | Request stop for the current turn and switch to the selected session |
| `/stop`, `/cancel`, `/stop task:TOKEN` | Stop the user's current or quoted turn, or a specific turn from an authorized administrator's direct chat |
| `/cd [ABSOLUTE_PATH]`, `/ws` | Inspect or switch project and manage workspace aliases |
| `/cd list [PAGE]`, `/cd use PROJECT_ID` | Browse and select an authorized project in the current route's T3 environment from an admin direct chat |
| `/review QUESTION`, `/result [TOKEN] [PAGE]` | Use the independent review route or read a redacted current or past result |
| `/doctor`, `/doctor --echo [ROUTE_ID]` | Read known connection state or run a bounded model check |
| `/reconnect`, `/reconnect --wait`, `/reconnect --cancel` | Reconnect the current app, wait for active work, or cancel the wait as an admin |
| `/timeout`, `/timeout deadline SECONDS`, `/timeout idle MINUTES` | Read or set a 1–86,400 second deadline and a 1–120 minute idle timeout. Use `off` to disable either, and `default` to inherit idle timeout |
| `/config`, `/config run`, `/config display` | Admin settings and display controls in an authorized direct chat |
| `/config route [PAGE]`, `/config route use ROUTE_ID` | Browse or set the user's default route in the direct chat |
| `/config access`, `/config access user`, `/config access admin`, `/config access route ROUTE_ID` | Inspect effective access, startup values, and live overrides |
| `/config groups [PAGE_OR_CHAT_ID]` | View joined groups and confirm group ACL or mention settings |
| `/config mention check`, `/config mention authorize` | Verify or request all-group-message access for the app |
| `/config identity`, `/config identity use ROUTE_ID` | Browse or select a dedicated tool identity route |
| `/config reply MODE`, `/config tools MODE`, `/config process MODE` | Change future reply, tool, and process display settings |
| `/config idle-timeout MINUTES`, `/config concurrency`, `/config progress` | Set default idle time, dispatch budget, and progress display |

In a direct chat, `/config route` shows up to six eligible routes per page. Wait for current and queued work to settle before changing it. Explicit routing, quotes, and a configured classifier still follow the normal dispatch rules. Switching back can recover the previous session for that route.

`/ws list` shows eight aliases per page. Deleting an alias leaves its project, directory, and session intact. `/cd` uses an absolute path on the execution host, including a drive or UNC path on Windows. To stop current work while switching, use `/cd --stop PATH`, `/ws use --stop NAME`, or `/resume --stop TOKEN`. The original stop remains linked to its original reply.

### Reply and progress modes

- `card` replies update a result card. `text` sends final plain text. `markdown` sends final Markdown with headings, lists, and code blocks. Quote a final reply to continue, or use `/result`.
- Process mode `off` disables the independent process view. `brief` and `detailed` show phase and tool status. `stream` shows verified public assistant text. `stream-brief` and `stream-detailed` combine text and tool state in the same process card. The brief display shows one tool entry and the detailed display up to five.
- Process mode `card` puts the preview in the original result card when card progress is enabled. Both preview styles are bounded to 1,600 characters. The final reply is delivered at completion. `/config tools default` restores inherited tool activity behavior.
- The preview and final reply can include the latest verified completed T3 plan draft. Long output is available through paged `/result`. Approvals and questions remain in T3.
- Without a process card, plain-text and Markdown turns can show a typing reaction on the original message until the reply settles. Message reaction permission controls that indicator.

`/status` shows the run, queue, approval, child work, and delivery state. Administrators see receiver capacity from an authorized direct chat. The Bridge keeps following delegated work after a parent run finishes. A verified T3 HTTPS link opens the original session. Card actions check the original requester and scope and expire after 55 minutes.

Deadline changes restart the absolute timer. Idle changes resample activity. An omitted subcommand with a numeric timeout uses seconds. To migrate an old minute-based value, use `/timeout idle MINUTES`. Approval, active child work, or unknown state interrupts the idle window, and restart begins a new observation window.

Quotes, forwards, and earlier topic messages can include supported Feishu card text. The Bridge verifies their original chat and topic. A brief gateway failure or timeout shares one retry per read operation. Permission or persistent read failure produces a notice before dispatch.

### Live access and group mentions

Use `/invite group` and `/remove group` to change group access. `/config groups` opens joined-group details in an authorized administrator's direct chat. Each confirmation rechecks membership and the state shown by the card. `/config mention all on` or `off` changes the app default. Use `/config mention on`, `off`, or `default` in an authorized group for an override.

Turning off a Bridge mention requirement still requires Feishu `im:message.group_msg` and a published `im.message.receive_v1` subscription for actual delivery. Use `/config mention check` or `/config mention authorize`, then send a fresh test message. Check the runtime group page for missing permission, missing subscription, or an unpublished app. [Feishu long-connection events](https://open.feishu.cn/document/ukTMukTMukTM/uYDNxYjL2QTM24iN0EjN/event-subscription-configure-)

For live batch access, use `/invite user`, `/remove user`, `/invite admin`, and `/remove admin` with actual mentions or app-specific Open IDs. `/invite route` and `/remove route` take the full route ID followed by users. Invalid entries or an attempted owner revocation cancel the batch. Revocation also blocks matching queued work. `/config run` updates only the selected app's settings and rejects a stale review.

### Stop a specific turn or run a model check

Use `/status tasks` with an optional chat or topic filter from an authorized admin direct chat. Copy the current token and send `/stop task:TOKEN`. The stop targets the original run. The administrator receives an acknowledgment, while the original conversation receives its final outcome. Queued runs are cancelled and active runs interrupted by ID. If T3 still has delegated or background work, the Bridge continues checking the original run and retains its capacity allocation.

`/doctor --echo ROUTE_ID` creates one independent fixed-prompt diagnostic on an eligible `plan` and `approval-required` route. It checks for an exact `OK`. A group or topic invocation is acknowledged there and the result is delivered to the requester by the same app in direct chat. The app and route permission are checked again at dispatch and delivery. Each app permits one active diagnostic. A user may start one per 30 seconds, with a one-minute shared queue and run limit.

## Meeting controls

See the [meeting configuration](CONFIGURATION.md#document-comments-and-meetings) for opt-in switches. With `meetingEnabled`, an owner or admin can send the commands below in direct chat.

| Command | Behavior |
| --- | --- |
| `/meeting status` or `/meeting` | Get the operator's tracked meetings |
| `/meeting join MEETING_NUMBER` | Request to join a nine-digit meeting |
| `/meeting leave [MEETING_NUMBER]` | Leave using the recorded long meeting ID |
| `/meeting confirm-left MEETING_NUMBER` | Close a pending receipt after verifying the bot has already left |

After a join acknowledgment, confirm the bot in the participant list or waiting room. A matching end event can close an earlier pending join once the long meeting ID is verified. Unknown acknowledgments retain their receipt and are not resubmitted blindly.

On the host, open **Meetings** under the selected receiver app or press `H` in TUI. Use the full process ID and app identity.

```sh
t3-bridge config live-meetings -process PROCESS_ID -app-id APP_ID -tenant feishu
t3-bridge config live-meetings -process PROCESS_ID -app-id APP_ID -tenant feishu \
  -action join -number MEETING_NUMBER -confirm
t3-bridge config live-meetings -process PROCESS_ID -app-id APP_ID -tenant feishu \
  -action leave -number MEETING_NUMBER -revision REVISION -confirm
t3-bridge config check-meeting -profile work -app-id APP_ID
```

Use `-tenant lark` when applicable. A verified `ended` result closes the join receipt successfully. If Feishu shows the bot has left or been removed, close an uncertain receipt with `-action confirm-left` and the current revision. Host-initiated joins have no originating Feishu direct chat. For automatic notes to a specific direct chat, start from that authorized chat.

`config check-meeting` can read a saved app even with the receiver stopped. It checks join and leave, events and captions, and in-meeting message permissions. It reports live app publication and invitation, activity, and ended-event subscriptions. Add `-probe-availability` to check the app owner's active meeting endpoint. Host approval and actual event delivery are verified with a dedicated meeting.

An allowed owner or admin can invite the bot when `meetingAutoJoinUsers` is configured. The Bridge checks app, bot, and inviter identity, accepts invitations no older than two minutes, and deduplicates join attempts by call ID across restarts. Use `/meeting status` in direct chat to inspect its receipt.

### Captions, Q&A, and notes

With a configured meeting route, use the same authorized direct chat. Supply the meeting number when there is more than one tracked meeting.

| Command | Behavior |
| --- | --- |
| `/meeting transcript [NUMBER]` | Get a bounded, deduplicated caption excerpt, with an indication if reads are incomplete |
| `/meeting ask [NUMBER] QUESTION` | Send the question and bounded captions to the meeting's T3 session |
| `/meeting notes [NUMBER]` | Summarize decisions, actions, and open questions in the same meeting context |
| `/meeting stop [NUMBER]` | Stop the original processing turn |

Captions merge live events with paged reads and preserve revised lines. The cache clears on leave or a verified end. Private Q&A replies to the initiating direct chat. Quote a question with `/stop` or a reply with `/result` as needed.

With `meetingRoomReplies`, an authorized participant can use `@bot /ask QUESTION`, `@bot /notes`, and `@bot /stop` in the meeting. Each speaker has a separate session. Verified results are posted to the room only after the run and delegated work settle. An ambiguous delivery is retried with its original UUID within the delivery window.

With `meetingSummaryTarget`, an end event starts a separate summary for a verified recipient. The Bridge saves a bounded caption excerpt and attempts additional paged reads. Missing captions, an unknown recipient, or revoked access can prevent it from running or sending. A valid pending delivery uses the original recipient, body, and UUID for up to 55 minutes. When sending to the app owner, the Bridge verifies the current owner before delivery.

## Troubleshooting

- **Messages are not received.** Check app publication, event subscription, long-connection state, ACLs, and group mention rules, then use a new test message. For a topic, the app must be able to read its group and message. A known topic chat needs a resolvable topic before queuing. If configured, the topics allowlist applies before dispatch.
- **The receiver or credential changed.** Verify `receiver-readiness` for the saved revision and each selected app. For a rotated secret at the same path, reconnect the app and check the next sample. A new path or route requires restarting the receiver.
- **Dispatch or stop is ambiguous.** Preserve the private state directory and read the original message receipt and T3 thread, request, and run. The Bridge keeps pending reconciliation and avoids duplicating actions.
- **Monitoring or reports need checking.** Verify the signed event source and the configured route, report source, and delivery status independently. [HomeOps integration](HOMEOPS-INTEGRATION.md)
