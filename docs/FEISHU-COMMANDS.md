# Feishu slash commands

English | [简体中文](FEISHU-COMMANDS.zh-CN.md)

Type commands in the Feishu or Lark conversation with the T3 Bridge bot. In a group or topic, use a Feishu @mention of the bot when the group requires it. Commands run against the app that received the message. Send `/help` there for a quick reference.

In the syntax below, text in brackets is optional, uppercase words stand for your values, and a page number starts at 1. Copy IDs and tokens from the corresponding command reply. An **admin direct chat** is an authorized one-to-one chat with the bot for the current Feishu app's owner or administrator. When using a management command that lists or selects items, send it as a new message in that chat. Route, chat, topic, and user access still apply where relevant.

- [Chat, sessions, and results](#chat-sessions-and-results)
- [Routes, projects, and workspaces](#routes-projects-and-workspaces)
- [Timeouts and runtime settings](#timeouts-and-runtime-settings)
- [Group access and permissions](#group-access-and-permissions)
- [Receiver diagnostics and control](#receiver-diagnostics-and-control)
- [Document comments](#document-comments)
- [Meetings](#meetings)

## Chat, sessions, and results

| Command | What it does |
| --- | --- |
| `/help` | Show the bot's command list. Some entries depend on current permissions and app configuration. |
| `/status` | Show the current or quoted turn's route, session, run, approval, child work, and delivery state. An admin direct chat also shows receiver capacity. |
| `/new`, `/reset` | Archive the current session mapping, request stop for any active turn, and start a fresh T3 context on the next message. Previous sessions remain in history. |
| `/new chat [NAME]` | Create a private Feishu group for the current authorized route and project. Names can be up to 60 characters. The default is `T3 Bridge · ROUTE_ID`. The app needs group creation permission. |
| `/resume`, `/resume page PAGE` | List the user's resumable sessions for the current chat or topic, route, and project. Each page shows up to eight sessions. |
| `/resume TOKEN`, `/resume use TOKEN` | Select a session using the short token returned by `/resume`. |
| `/resume --stop TOKEN` | Request stop for the current turn and switch to the selected session. Send it directly in the current workspace. |
| `/result` | Read the current turn's redacted final result. Reply to your own earlier result to read that turn instead. |
| `/result TURN_TOKEN [PAGE]` | Read a specific result by its round token or full request ID. Use the token returned by `/result` for later pages. |
| `/input` | Reply to your own latest continuable result card to open a text field on that card. Submit the text for continued discussion. For a plain-text reply, send a follow-up or quote the reply. T3 questions and approvals remain in T3. |
| `/review [QUESTION]` | Request an independent read-only review through the configured review route. Reply to your own result to supply context. You can omit the question when reviewing an available turn. The review has its own result and does not change your selected session. |
| `/stop`, `/cancel` | Request stop for your current turn. To target another active turn of yours, reply to its latest card or request, including a review. A stale quote is rejected. Check `/status` for the outcome. Stopping a run does not roll back work. |
| `/status tasks [CHAT_ID[:TOPIC_ID]] [PAGE]` | In an admin direct chat, list up to eight turns per page for this app. Filter by a full Feishu chat ID starting with `oc_` and, optionally, a topic ID. A page number alone is also accepted. |
| `/stop task:TOKEN` | In an admin direct chat, request stop for the exact turn using its token from `/status tasks`. The administrator receives an acknowledgment and the original conversation receives the outcome. |

A session token selects a T3 conversation. A result token selects one round of output. A `task:` token addresses a turn for admin stop. For example, reply to your result card with `/result`, then copy its round token into `/result TURN_TOKEN 2`.

## Routes, projects, and workspaces

A route defines where a message can run and which project, model, and interaction settings it uses. The workspace commands operate within the authorized route. An absolute path refers to the execution host and may be a Windows drive or UNC path. Send route selection commands as new direct messages to the bot.

| Command | What it does |
| --- | --- |
| `/ROUTE_ID MESSAGE` | Send a message to a specific authorized route. The configured hostname or a unique route alias can also follow the leading slash. Use `/review` for the independent review route. |
| `/config route [PAGE]` | In an authorized direct chat, browse the projects and models available to you. Each page shows up to six routes. |
| `/config route use ROUTE_ID` | Set your default route in this direct chat. Wait for current and queued work to settle first. Explicit routing and quoted replies still follow the normal routing rules. |
| `/cd` | Show the current project. |
| `/cd ABSOLUTE_PATH` | Switch to an existing directory on the execution host. The Bridge can register it with T3 if this route and T3 environment allow registration. |
| `/cd --stop ABSOLUTE_PATH` | Request stop for the current turn and switch projects. Send it directly, without quoting a message. |
| `/cd list [PAGE]`, `/cd use PROJECT_ID` | In an admin direct chat, browse registered projects in the current route's T3 environment and select one by its project ID. |
| `/ws`, `/ws list [PAGE]` | Browse your named workspaces within the current scope. Each page shows up to eight aliases. |
| `/ws save NAME` | Save the current project under a workspace name. Names can contain spaces and use up to 64 visible characters. |
| `/ws use NAME` | Select a saved workspace. Use `/ws use -- NAME` to treat a leading `--stop` or `--` literally. |
| `/ws use --stop NAME` | Request stop for the current turn and select the saved workspace. Send it directly, without quoting a message. |
| `/ws remove NAME`, `/ws rm NAME` | Remove the saved alias. The project, directory, and session remain available. |
| `/config identity`, `/config identity use ROUTE_ID` | In an admin direct chat, view or select a configured tool identity route for the current project. The routes must use compatible local T3 bindings and pass the current access checks. |

If a configured route name overlaps with `account` or `doc`, that name takes precedence over the built-in command. Use a distinct route name to expose both commands.

## Timeouts and runtime settings

### Timeouts for the current scope

The Bridge scopes timeout overrides to the requester, chat or topic, route, and project. Send the command directly or reply to your latest turn. Changes can affect the active run. A deadline update starts a new timer, and an idle update restarts the activity observation window.

| Command | What it does |
| --- | --- |
| `/timeout`, `/timeout deadline`, `/timeout idle` | Show the current timeout settings and open a settings card when available. |
| `/timeout deadline SECONDS`, `/timeout SECONDS` | Set an absolute deadline of 1–86,400 seconds. The shorter form also uses seconds. |
| `/timeout deadline off`, `/timeout off` | Disable the absolute deadline. |
| `/timeout idle MINUTES` | Set a 1–120 minute idle limit. Approval, activity, active child work, and unknown state interrupt the idle window. |
| `/timeout idle off` | Disable the idle timeout for this scope. |
| `/timeout idle default`, `/timeout default` | Inherit the app's default idle timeout. |

### App settings

The app owner or administrator can change runtime settings. Use an admin direct chat for settings cards.

| Command | What it does |
| --- | --- |
| `/config`, `/config run` | Open the runtime settings form in direct chat. In a group, bare `/config` shows the access summary. |
| `/config display` | Open the display settings card for reply and process modes. |
| `/config concurrency N` | Set the app's dispatch budget from 0 to 50. Zero disables this Bridge limit. Lowering it leaves active work running. |
| `/config progress on`, `/config progress off` | Turn visible progress updates on or off. |
| `/config reply [MODE]` | Show or set the reply mode. Choose `card` for an updated result card, `text` for final plain text, or `markdown` for formatted final text. |
| `/config tools [MODE]` | Show or set tool activity detail. Choose `off`, `brief`, `detailed`, or `default` for inherited behavior. |
| `/config process [MODE]` | Show or set the process view. Choose `off`, `brief`, `detailed`, `stream`, `stream-brief`, `stream-detailed`, or `card`. |
| `/config idle-timeout [MINUTES]`, `/config idle-timeout off` | Show or set the app default idle timeout. Use 1–120 minutes or `off`. Individual timeout overrides take precedence. |

Process modes `brief` and `detailed` show phase and tool status in a separate card. `stream` shows verified public assistant text. `stream-brief` and `stream-detailed` add tool status to that view. `card` puts the preview in the result card when card progress is enabled. [Reply and progress modes](RUNBOOK.md#reply-and-progress-modes)

## Group access and permissions

These commands require the app owner or administrator. Use the target group and @mention the bot for group-specific commands. Account-wide settings apply to the receiving app.

| Command | What it does |
| --- | --- |
| `/config access` | Show an access summary, including the app's group mention default and the current group's settings when sent there. |
| `/invite group`, `/config group on` | Enable Bridge access in the current group. |
| `/remove group`, `/config group off` | Disable Bridge access in the current group. |
| `/invite all group` | Enable access for every group the bot has already joined. Use it from a direct chat or group. |
| `/config groups [PAGE_OR_CHAT_ID]` | In an admin direct chat, browse joined groups, or open one by full `oc_` ID. Confirm any changes to access or mentions on the group card. |
| `/config mention on`, `/config mention off`, `/config mention default` | In the target group, require a bot mention, turn that requirement off, or inherit the app default. |
| `/config mention all`, `/config mention all on`, `/config mention all off` | Show or set the mention default. Use a direct chat or group. Explicit group overrides still apply. |
| `/config mention check`, `/config mention authorize` | In an admin direct chat, check or request the Feishu app's `im:message.group_msg` scope. Follow the returned card or the Feishu Open Platform instructions. |
| `/config access user [PAGE]`, `/config access admin [PAGE]` | In an admin direct chat, list user or administrator access, including startup settings and live overrides. |
| `/config access route ROUTE_ID [PAGE]` | In an admin direct chat, list the effective user access and live overrides for a specific route. |
| `/invite user USER [USER ...]`, `/remove user USER [USER ...]` | Grant or revoke app-level user access. |
| `/invite admin USER [USER ...]`, `/remove admin USER [USER ...]` | Grant or revoke the administrator role for this app. |
| `/invite route ROUTE_ID USER [USER ...]`, `/remove route ROUTE_ID USER [USER ...]` | Grant or revoke users' access to a specific route. Supply the full route ID before the users. |

For a `USER`, insert an actual Feishu user mention or an Open ID starting with `ou_` for this app. A batch with an invalid entry or a request to revoke the owner's access is rejected. App, group, user, and route access are evaluated separately. Any production write permissions and T3 approvals are managed in their own systems.

For messages without an @mention, Feishu must grant `im:message.group_msg` and the app must publish the `im.message.receive_v1` subscription. After changing settings, send a fresh group message to verify delivery. [Live access and group mentions](RUNBOOK.md#live-access-and-group-mentions)

## Receiver diagnostics and control

| Command | What it does |
| --- | --- |
| `/doctor` | Show known receiver, connection, route, and queue state. Use an echo to test the model path. |
| `/doctor --echo [ROUTE_ID]` | Run a fixed-prompt model check for an authorized `plan` and `approval-required` route. The owner or admin can invoke this in an authorized direct chat, group, or topic. If the current route is unsuitable, supply an eligible route ID. A group or topic gets an acknowledgment, and the same app sends the result privately to the requester. |
| `/reconnect` | As owner or admin, reconnect the current Feishu app. Use it after rotating the secret at the existing configured path. Changes to the configured path or route require restarting the receiver on the host. |
| `/reconnect --wait` | As owner or admin, pause dispatch of new work for this app, wait for in-flight work to settle, and reconnect. Check progress with `/doctor`. |
| `/reconnect --cancel` | As owner or admin, cancel a pending reconnect wait. |
| `/ps` | Show this app's registered receiver instance ID, PID, and start time. |
| `/exit INSTANCE_ID` | In an admin direct chat, request shutdown of this single-app receiver after the acknowledgment is sent. Copy its full instance ID from `/ps`. Use host controls for a shared receiver. |
| `/account` | In an admin direct chat, show the receiving App ID, tenant, and known connection state. |
| `/account change` | In an admin direct chat, get host workbench instructions for replacing the app. Keep the App Secret in a private credential file on the host. |

The model check uses an independent turn and expects exactly `OK`. Each app allows one pending or active check, each user can start one every 30 seconds, and the queue and run share a one-minute limit. [Stop a specific turn or run a model check](RUNBOOK.md#stop-a-specific-turn-or-run-a-model-check)

## Document comments

When `commentRoute` and document permissions are configured, mention the bot in a supported Feishu document comment or comment reply. Its answer goes back to the document. Send these management commands in the bot conversation.

| Command | What it does |
| --- | --- |
| `/doc` | Check whether document comments are enabled and get usage instructions. |
| `/doc tasks [PAGE]` | In an admin direct chat, list the current app's comment turns and their control tokens. |
| `/stop comment:TOKEN` | In an admin direct chat, request stop for the exact comment turn using its token from `/doc tasks`. |
| `/timeout comment:TOKEN`, `/timeout comment:TOKEN MINUTES_OR_MODE` | In an admin direct chat, show or set the comment thread's idle timeout. Use 1–120 minutes, `off` to disable it, or `default` to inherit the app default. The command can also update the active turn. |

If a token is stale, run `/doc tasks` again to verify the target. [Document comments setup](RUNBOOK.md#document-comments)

## Meetings

### Direct chat

Use an admin direct chat for meeting controls. New joins require `meetingEnabled`. Use the same direct chat that manages the receipt. When a command allows the meeting number to be omitted, the Bridge needs exactly one matching receipt for you. Use a nine-digit meeting number and keep it in one word for `/meeting ask`. Keep meeting passwords out of command messages.

| Command | What it does |
| --- | --- |
| `/meeting`, `/meeting status` | Show your tracked meeting receipts. |
| `/meeting join NUMBER` | Request that the bot join the meeting. Check the participant list or waiting room after the acknowledgment. |
| `/meeting leave [NUMBER]` | Request that the bot leave the tracked meeting. |
| `/meeting confirm-left NUMBER` | Close the saved receipt after you verify in Feishu that the bot is absent from the meeting and waiting room. |
| `/meeting transcript [NUMBER]` | Read a bounded caption excerpt. The reply indicates when the caption read was incomplete. |
| `/meeting ask [NUMBER] QUESTION` | Send the question and available captions to the meeting's T3 session. The result is sent to the initiating direct chat. |
| `/meeting notes [NUMBER]` | Request a summary of decisions, actions, and open questions in the same meeting context. |
| `/meeting stop [NUMBER]` | Request stop for the original meeting processing turn. |

Captions, questions, and notes require an authorized `meetingRoute` and a verified join. You can quote your private meeting result with `/result`. [Meeting controls and setup](RUNBOOK.md#meeting-controls)

### In-meeting chat

When `meetingRoomReplies` is enabled, an authorized participant can use `@bot` or `@BOT_NAME` at the start of the meeting chat message. Each speaker has a separate meeting session, and completed answers are posted to the room.

| Command | What it does |
| --- | --- |
| `@bot /ask QUESTION` | Ask a question using the meeting captions. |
| `@bot /notes` | Request meeting notes. |
| `@bot /stop` | Stop your current turn and earlier queued meeting requests. `@bot stop` and `@bot 停止` also work. |
