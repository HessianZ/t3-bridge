# 配置

[English](CONFIGURATION.md) | 简体中文

启动和四个配置入口默认使用已选中的命名配置。未设默认时，优先使用当前目录下已有的 `config/local.json`，否则使用系统用户配置目录下的 `t3-bridge/config.json`。可用 `-profile` 指定名称，或用 `-config` 指定其他 `.json` 文件。`-check` 和运行入口共用严格解析与业务校验。检查只读取配置文件。

在交互终端直接执行 `t3-bridge run`，所选默认配置文件尚不存在时，会打开 TUI 的首次配置指引。退出工作台后，再用已保存配置执行预检、确认启用和启动。非交互终端中的缺档提示会列出配置入口。

## 命名配置

```sh
t3-bridge profile create work
t3-bridge profile create review -from work.candidate.json
t3-bridge profile list
t3-bridge profile use work
t3-bridge config web -profile work
t3-bridge run -profile review -check
t3-bridge profile export work > work.candidate.json
```

每个命名配置保存在用户配置目录的 `t3-bridge/profiles/<名称>/config.json`。从模板创建或从已有 JSON 克隆时，每个账号都获得独立的状态目录，接收授权初始关闭。Web 与桌面工作台的「管理命名配置」可创建、编辑、导出、设为默认、归档和恢复配置。

`profile use` 影响后续使用默认配置的命令。安装的后台服务保留安装时选定的配置路径。`profile archive <名称>` 会归档配置和备份，保留状态目录。归档默认配置时会清除默认选择。运行中的命名配置需先停止。用 `profile restore <归档输出的路径>` 恢复原配置及已有状态目录的关联，再按需设为默认。

### 永久清除与私有导出

用 `profile purge work` 预览永久清除的路径、需保留的引用和清除后的默认选择。确认时可传入预览的版本。

```sh
t3-bridge profile purge work
t3-bridge profile purge work -confirm -revision "$PREVIEW_REVISION"
```

兼容旧桥的 `profile remove work --purge --yes`。永久清除涉及所选命名配置及可确认归属的受管状态。自定义状态目录和外部凭据按预览所列保留。继续使用可恢复的归档时，执行 `profile archive work`。

Web 和桌面的「管理命名配置」提供「预览永久清除」和「私有导出」。TUI 按 `F4` 选择命名配置，再按 `x` 预览清除，或按 `e` 选择私有导出位置。TUI 清除需复输配置名称，私有导出在确认页按 `y` 写入。

普通导出可写到 stdout，或用 `-output` 指定本机 JSON。含凭据导出经显式确认后，将配置和各 App 的明文 App Secret 写入私有目录中的 JSON 文件。导出文件使用当前用户专属权限。

```sh
t3-bridge profile export work -include-secrets -yes -output "$PRIVATE_BUNDLE"
t3-bridge profile import work-copy -from "$PRIVATE_BUNDLE"
```

需要先核对来源、App 和目标时，运行 `profile import work-copy -from "$PRIVATE_BUNDLE" -preview`。提交时可用 `-expected-revision` 绑定预览返回的 `Revision`。

Web 和桌面在「命名配置」里选择「导入私有包」。TUI 在 `F4` 命名配置页按 `i`，输入来源路径和新配置名，在预览中按 `y` 确认。导入成功后可从清单打开新配置。

导入会创建新的命名配置、状态位置和独立 App Secret 文件，接收授权初始关闭。T3 MCP 授权继续使用配置中的文件引用。跨主机导入后，按目标环境核对 T3 项目及授权。

### 跨主机导入私有包

导入会按目标主机规则重建 Bridge 管理的凭据和状态路径。目标主机的 T3 配置有变化时，可提供一个本机 JSON 调整文件。格式与 `config patch` 一致，例如以下文件把单路由配置指向目标 Mac 上的 T3 环境和项目。

```json
[
  {"path":["mcp","tokenFile"],"value":"/Users/me/.config/t3-bridge/credentials/t3-oauth.json"},
  {"path":["mcp","environmentId"],"value":"target-environment"},
  {"path":["routes","work","environmentId"],"value":"target-environment"},
  {"path":["routes","work","projectId"],"value":"target-project"}
]
```

用目标主机实际的绝对路径、环境和项目值替换示例。若来源还保存了项目路径或本机工具配置，按实际目标一并调整。若从 `mcp.runtimeFile` 改用 `mcp.endpoint`，在同一个调整文件中删除前者并设置后者。附加 App 的字段路径以 `additionalAccounts` 和从零开始的索引开头。App 身份与 Bridge 管理的字段由导入流程保留或生成。

导入 Windows 时，调整文件中的 Windows 绝对路径遵循 JSON 转义规则。例如令牌路径写作 `"C:\\Bridge 数据\\t3-oauth.json"`。如需修改第一个附加 App 的令牌引用，字段路径写作 `["additionalAccounts","0","mcp","tokenFile"]`。将导出包复制到目标机器的用户私有目录，再按目标机器的 App、T3 环境和项目准备各自的调整。T3 令牌文件由目标机器单独准备。

```sh
t3-bridge profile import work-copy -from "$PRIVATE_BUNDLE" -patch "$TARGET_EDITS" -preview
t3-bridge profile import work-copy -from "$PRIVATE_BUNDLE" -patch "$TARGET_EDITS" -expected-revision "$PREVIEW_REVISION"
```

Web 和桌面的「导入私有包」接受同一个调整文件。TUI 可在新配置名称页按 `Ctrl+E` 输入调整文件路径，返回名称页后按 Enter 预览。预览显示调整的字段，确认时核对私有包和调整文件各自的版本。导入完成后，在目标主机核对 T3 授权、项目和模型，再按独立实例流程验证接收。 [独立实例验收](RUNBOOK.zh-CN.md#独立实例验收)

### 转接旧桥的私聊 T3 会话

用旧 T3 profile 的私聊目录条目转接原线程。先停止旧接收端，并确认原线程已空闲。选择同一租户和 App，准备已知私聊 `oc_`、发起人 `ou_`，以及已授权且项目路径与旧目录一致的目标路由。

在 CLI 和 Web 留空 App 时使用已保存配置的主 App。TUI 使用当前选定的 App。省略旧 profile 名称时，只采用同 App、同租户下唯一匹配的旧 T3 profile。有多个匹配时请填写旧名称。

如果还没有原线程 ID，用 `-list` 盘点旧目录中的聊天和话题范围、活动线程数及最近更新时间。加 `-chat-id` 可按完整聊天 ID 查看活动线程及原项目路径。旧目录无法证明聊天类型或参与者，继续转接前请核对它是自己的原私聊。查询只读取旧目录与已保存的目标配置。

```sh
t3-bridge config import-session -profile work -list
t3-bridge config import-session -profile work -chat-id "$CHAT_ID" -list
```

选好线程后，生成转接预览。

```sh
t3-bridge config import-session -profile work -app-id "$APP_ID" \
  -legacy-profile "$LEGACY_PROFILE" \
  -thread-id "$THREAD_ID" -chat-id "$CHAT_ID" -user-id "$USER_ID" -route work
```

命令沿用旧桥配置导入的本机路径发现，可用 `-legacy-config` 指定其他来源。它从旧 `config.json` 同目录下的 `profiles/<名称>/sessions.json.catalog.json` 读取所选 profile 的活动 T3 条目。核对预览的 App、私聊、发起人、环境、项目和原线程，停止目标接收端后用相同参数追加 `-confirm "$REVIEW"`。现有会话和在途输入需要先在原范围处理。 [旧桥配置导入](#从-lark-channel-bridge-导入配置)

已用 `/new` 归档当前会话且没有在途工作的范围可重新预览并登记。确认后的首条匹配输入才会使用登记的原线程。归档前生成的预览失效。旧桥 T3 适配器的 `read-only` 模式对应 T3 的 `approval-required` 和 `plan`。目标路由以原线程实际读取的模式、模型和选项为准。

Web 和桌面在「转接旧会话」留空聊天 ID 时可盘点范围。确认私聊后，填入其 ID 并查询线程，再在原线程 ID 栏选择候选或直接输入。TUI 打开已保存的配置并选定 App 后按 `S`。按 `F3` 盘点范围，选择已确认的原私聊或手工填入 ID，再按 `F3` 查找线程。选中线程后按 Enter 填入。填写发起人和路由后按 `Ctrl+S` 预览，再按 `s` 确认写入。

登记前可在 Web 或桌面选「预览并核验 T3」，在 TUI 按 `Ctrl+T`，或在 CLI 的预览命令追加 `-check-t3`。核验用所选 App 已保存的 T3 连接读回原项目、模型、选项及权限模式，有差异时并列显示路由值和原线程值。CLI 在 `t3Check` 返回结果。该次核验预览仅在配置匹配、线程空闲且路由通过 T3 派发预检时提供写入确认。选择匹配路由或修订并保存后，可重新核验。T3 暂不可用时仍可使用离线预览。首条匹配输入继续执行运行时核验。

若已选路由指向原 T3 项目和目录，可在 Web 或桌面点「采用原线程执行配置并审阅」，在 TUI 按 `Ctrl+R`，或在命令中加 `-prepare-route` 输出配置草稿。Bridge 只读核对线程空闲、T3 派发权限、模型能力和群聊来源。草稿只调整所选路由的 driver、provider、模型、选项、运行模式和交互模式。若已有飞书工具绑定，草稿要求 driver 和 provider 与该绑定一致。

在正常配置差异页确认保存，再回到转接表单重新预览和登记。CLI 保存前先核对 `config diff`，用生成草稿前的配置修订号执行 `config save -expected-revision`。配置保存和会话登记是两次独立确认。

启动目标接收端后，在同一私聊向已登记的路由发送新消息。若该路由当前未选中，在消息开头加 `/路由名`。Bridge 在提交前用 T3 只读接口确认原项目路径、provider、模型、选项、运行权限及空闲状态。通过后在原线程创建新 run，再用原入站 ID 查询回执。发送结果未知时沿原请求核对。

用户可用 `/new` 丢弃当前范围的待转接记录。旧目录保存的原 run、回复和状态继续由原数据保留。 [消息回执查询](RUNBOOK.zh-CN.md#按飞书入站消息核对回执)

### 转接旧桥的群聊和话题

沿用私聊的停机、预览和确认步骤。选中原群后，明确填写群聊类型及原聊天 ID。原线程属于话题时，再填写完整话题 ID。发起人必须具有该群或话题及目标路由的权限。

```sh
t3-bridge config import-session -profile work -chat-type group \
  -chat-id "$CHAT_ID" -topic-id "$TOPIC_ID" -list
t3-bridge config import-session -profile work -chat-type group \
  -chat-id "$CHAT_ID" -topic-id "$TOPIC_ID" -thread-id "$THREAD_ID" \
  -user-id "$USER_ID" -route work
```

普通群聊省略 `-topic-id`。Web 和桌面在「转接旧会话」选择群聊，TUI 在 `S` 表单填写 `group`，也可以用 `F3` 选择原话题。核对预览后确认。启动目标接收端，由所选发起人在原群或话题按当前 @ 规则发送新消息。

选用「预览并核验 T3」时，群聊和话题还会读取原线程的 T3 历史，核对其中的旧桥来源、发起人和范围。发现其他发言人或无法核对来源时，本次核验预览不提供写入确认。CLI 在 `t3Check` 返回结果，通过时的 `historyMessageCount` 是已核对的原消息引用条数。首条续接继续通过飞书核对原消息与当前机器人身份。

首次接续前，Bridge 读取原 T3 线程在公开消息视图中的全部输入，最多 8 页，每个输入正文最多 20,000 个 UTF-16 码元。它通过当前 App 逐条核对旧输入关联的飞书原消息，最多 128 条，合并输入也逐条核对。所有原消息须属于同一群、话题及所选发起人。混合发送者、缺少可读原消息、超过核验范围或线程变化时，Bridge 停止接续。原线程保留在 T3，用户可用 `/new` 另开会话。

## 模板

| 场景 | 模板 |
| --- | --- |
| 新实例、单执行目标 | [单路由](../config/generic.example.json) |
| 多环境或独立复核 | [多路由](../config/multi-host.example.json) |
| 可信监控事件和维护查询 | [HomeOps 可选适配](../config/homeops.example.json) |

## 会议机器人

每个账号可单独设置 `meetingEnabled`，缺省为 `false`。在 Web 或桌面工作台的「飞书与接收端」勾选会议入会，在 TUI 编辑「会议」字段，在 CLI 使用 `config patch` 修改 `meetingEnabled`。开启后，该账号的 `meeting` 路由 ID、主机名和别名需使用其他名称，`/meeting` 用于会议控制。保存并重启接收端后生效。

请为需要入会的应用启用 `vc:meeting.bot.join:write`。应用仍需获得实际会议的放行。可订阅 `vc.bot.meeting_ended_v1`，由已核对应用和会议 ID 的结束事件结清回执。参会、字幕和自动投递的权限按场景检查。 [运行命令](RUNBOOK.zh-CN.md#手动会议控制)

配置账号的 `meetingRoute` 后可读取字幕并请求 T3 问答或纪要。路由需使用 `plan` 和 `approval-required`，并授权实际发起人。工作台和桌面窗口的「飞书与接收端」、TUI 的「会议」及 CLI 的 `config patch` 均可设置此字段。留空时保留手动入会和离会。

开启前请确认会议字幕可用于这些命令。订阅 `vc.bot.meeting_activity_v1` 后，Bridge 会在已跟踪会议中接收字幕推送，缓存最近 30 句，正文最多 6,000 字节。缓存只存内存，完整进程重启后可通过分页补读。发送问答或纪要时，有界摘录会保存在该 Bridge 的任务回执及对应 T3 会话中。`/meeting transcript` 的有限文本也保留在命令回执中。 [字幕与问答命令](RUNBOOK.zh-CN.md#字幕与会议问答)

`meetingRoomReplies` 缺省关闭。已开启会议并配置 `meetingRoute` 的账号可启用此字段，允许有应用与路由权限的实际参会发言人使用 `@bot /ask`、`@bot /notes` 和 `@bot /stop`。问答和纪要发到原会议，参会者可见。Web、桌面和 TUI 均提供此开关，CLI 可通过 `config patch` 修改。飞书应用需要会中消息发布权限。 [会中指令](RUNBOOK.zh-CN.md#会中公开问答) · [飞书发布接口示例](https://github.com/larksuite/cli/blob/main/shortcuts/vc/vc_meeting_message_send.go)

`meetingAutoJoinUsers` 缺省为空。已启用会议的账号可填写允许自动接受邀请的用户 Open ID，并订阅 `vc.bot.meeting_invited_v1`。只有名单中当前仍具备管理员或所有者权限的用户可触发自动入会。Web 和桌面工作台接受每行一个 ID，TUI 和 CLI 编辑 JSON 数组。清空名单并重启接收端可关闭自动入会。 [邀请回执与离会](RUNBOOK.zh-CN.md#自动接受会议邀请)

`meetingSummaryTarget` 缺省为空，关闭会后自动纪要。需要开启 `meetingEnabled`、配置 `meetingRoute` 并订阅 `vc.bot.meeting_ended_v1`。`origin` 投递到手动入会的原私聊。邀请入会缺少原私聊时，使用已核实且获会议路由授权的应用所有者。`owner` 始终使用已核实且获授权的应用所有者。发起人也须有会议路由权限。Web 和桌面工作台、TUI 及 CLI 均可修改。 [会后自动纪要](RUNBOOK.zh-CN.md#会后自动纪要)

## 配置工作台

```sh
t3-bridge config web
t3-bridge config gui
t3-bridge config tui
```

Web 入口在终端显示回环地址。macOS、Windows 和启用桌面构建的 Linux 可直接打开原生窗口。可选择命名配置或内置模板。指定 `-config` 可打开其他 JSON。新文件的首次配置指引串联飞书绑定、T3 授权、路由选择、预览保存和接收预检。

可用 `sh deploy/build-macos-config.sh` 构建 `bin/T3 Bridge Config.app`，在 macOS 13 或更新版本上从 Finder 打开。该入口沿用默认配置选择规则。关闭窗口后，菜单栏的链环图标可重新打开工作台。菜单中的「退出配置工作台」只关闭配置应用。从终端打开特定配置时使用 `config gui -profile ...` 或 `config gui -config ...`。

Linux 原生窗口使用 GTK 3 和 WebKitGTK 4.1。安装运行库后，解压完整桌面 ZIP 并运行包根目录的 `T3 Bridge Config.sh`。可用默认配置，也可传入 `-config` 或 `-profile`。二进制进入 `PATH` 后，可将包内的[桌面入口](../deploy/t3-bridge.desktop)放进用户的应用菜单目录。

```sh
sh "./T3 Bridge Config.sh" -config "/absolute/path/to/local.json"
```

从源码构建时，Debian 或 Ubuntu 环境可安装 `libgtk-3-dev`、`libwebkit2gtk-4.1-dev` 和 `pkg-config`。

```sh
CGO_ENABLED=1 go build -tags desktop -o bin/t3-bridge ./cmd/t3-bridge
bin/t3-bridge config gui
```

Windows 桌面版使用 Microsoft Edge WebView2 Runtime。解压完整桌面 ZIP 后，双击根目录的 `T3 Bridge Config.cmd` 打开工作台。它与终端的 `config gui` 使用相同的默认命名配置。若窗口打开失败，启动器留在终端显示错误。也可在解压目录执行 `bin\t3-bridge.exe config web`。 [WebView2 Runtime](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution)

普通构建提供 CLI、TUI、Web 与接收端。若需要从源码打开原生窗口，在 Windows PowerShell 用 `desktop` 标签构建。

```powershell
go build -tags desktop -o t3-bridge.exe ./cmd/t3-bridge
.\t3-bridge.exe config gui
```

Windows 生成的配置模板使用用户配置目录中的原生路径。已有凭据由运行账号所有，ACL 可包含该账号、SYSTEM 和本机管理员组。生成的私有文件只向当前运行账号授权。 [Windows 文件安全与访问权限](https://learn.microsoft.com/en-us/windows/win32/fileio/file-security-and-access-rights)

TUI 支持键盘浏览和编辑配置。用 Tab 切换账号、方向键选择字段、Enter 进入编辑。`a` 新增账号，`r` 新增路由，`p` 校验并查看差异，`b` 预览备份。按 `F4` 可打开、创建、设为默认、归档和恢复命名配置。切换或归档当前配置前会确认如何处理未保存草稿。

保存配置后，可在 TUI 按 `I`，或在 Web 和桌面工作台打开「安装后台服务」。安装和启动分别确认。 [用户级后台服务](RUNBOOK.zh-CN.md#用户级后台服务)

打开一条路由后，按 `F5` 查询并选择 T3 项目和模型。查询使用所选账号的 MCP 连接和该路由的环境 ID。留空可查看默认结果，也可按模型名称过滤。选择只更新草稿，保存前仍需核对选项、执行权限和差异。

### 选择机器人所在群

Web 和桌面工作台在各账号的「访问控制」提供「查询机器人所在群」。搜索群名或 ID，选中后点击「加入群聊草稿」。TUI 在当前账号按 `F8` 查询，按 `/` 搜索、空格勾选、Enter 确认。查询使用当前账号的应用身份和私有密钥文件，即使接收端尚未运行也可使用。已有群 ID 会保留。在 Web 或桌面修改完整 JSON 草稿后，请先校验再查询，以便确认选群所属账号。

CLI 可读取同一份可选群列表。

```sh
t3-bridge config list-groups -profile work -app-id cli_example
```

命令返回包含群 ID 和群名的 JSON 数组。省略 `-app-id` 时查询所选配置的主账号。所选群只更新该账号的 `chats` 草稿。校验并保存后，接收端重启时读取该配置。已有的运行时群授权优先，路由和用户权限照原规则检查。 [群聊管理命令](RUNBOOK.zh-CN.md#会话命令)

### 我的群

需要本机已安装 `lark-cli`。在 Web 或桌面工作台的「访问控制」展开「我的群」，点击「查询我的群」。首次使用时授权当前应用的所有者，再返回工作台核验。查群和搜索使用 `im:chat:read`。若从 Finder 打开的桌面应用找不到 `lark-cli`，可在能运行该命令的终端执行 `t3-bridge config gui`。

Windows 支持独立安装的 `lark-cli.exe` 和 `@larksuite/cli` 的 npm 安装。从「开始」菜单打开工作台时也可查找 `%APPDATA%\npm` 下的已安装程序。自定义 npm 前缀须位于宿主 `PATH`。Bridge 直接运行包内已安装的 `bin\lark-cli.exe`。 [npm 安装布局](https://docs.npmjs.com/cli/v11/configuring-npm/folders/#executables) · [lark-cli 原生程序路径](https://github.com/larksuite/cli/blob/v1.0.96/scripts/install.js)

选择一个群可查看机器人是否已加入。未入群时可额外授权 `im:chat.members:write_only`，核对应用和目标群后确认邀请。若显示待群审批或结果未确认，先重新查询入群状态。确认入群后可把群加入草稿，再校验并保存。App 已在线时，Web 和桌面可点「管理此群的在线授权」，直接查看该 App 与该群的当前权限。有多个匹配进程时先明确选择。离开配置页前可保存草稿。 [飞书邀请成员接口](https://open.feishu.cn/document/server-docs/group/chat-member/create)

TUI 在当前账号按 `F10` 打开「我的群」。选中机器人已加入的群后按 `a`，核对该群的在线授权，按 `Esc` 返回原选择。工作台和 TUI 共用私有授权配置。系统凭据库按 App 和用户保存用户令牌，使用相同 App 和用户的本机 `lark-cli` 可能共用该令牌。 [lark-cli 用户令牌索引](https://github.com/larksuite/cli/blob/v1.0.96/internal/auth/token_store.go)

CLI 提供同一流程，省略 `-app-id` 时使用主账号。打开 `start` 返回的 `url` 完成授权，再把返回的 `ticket` 交给 `finish`。邀请前将 `start` 的用途切换为 `invite`，确认授权后再提交邀请。

```sh
t3-bridge config my-groups start -profile work -app-id cli_example -purpose list
t3-bridge config my-groups finish -profile work -app-id cli_example -ticket "$ticket"
t3-bridge config my-groups list -profile work -app-id cli_example -query "项目讨论"
t3-bridge config my-groups invite -profile work -app-id cli_example -chat-id oc_example -confirm
```

下一页传入上次返回的 `nextPageToken` 和同一个搜索词。可以用 `status` 查看授权状态，用 `cancel` 取消当前票据。 [lark-cli 设备授权实现](https://github.com/larksuite/cli/blob/v1.0.96/cmd/auth/login.go)

### 即时群授权

也可在飞书的已授权管理员私聊发送 `/config groups`，点选当前应用所在群，再确认授权或 @ 规则。与本机入口共用同一份运行策略。 [私聊群管理](RUNBOOK.zh-CN.md#在私聊管理指定群)

在 Web 或桌面的「运行状态」打开「在线群授权」，选择接收进程和 App。可立即授权或撤销群聊、恢复配置文件中的群授权，也可修改默认 @ 规则及单群覆盖。选择「跟随应用」会清除单群 @ 覆盖。TUI 在当前账号按 `F9`，用 Enter 选操作，再按 `y` 确认。同一账号有多个运行匹配时，用 CLI 指定完整进程 ID。

```sh
t3-bridge ps
t3-bridge config live-groups -process PROCESS_ID -app-id cli_example -tenant feishu
t3-bridge config live-groups -process PROCESS_ID -app-id cli_example -tenant feishu -chat-id oc_example
t3-bridge config live-groups -process PROCESS_ID -app-id cli_example -tenant feishu -chat-id oc_example -access deny
```

新版接收端支持一次给最多 32 个已加入的群授权。Web 和桌面在完整群清单勾选群聊，审阅后确认。最近一次审阅可在五分钟内确认一次。TUI 在完整清单按空格勾选，按 `b` 审阅，再按 `y` 提交。CLI 重复传入完整群 ID，并加上 `-access allow -confirm`。

```sh
t3-bridge config live-groups -process PROCESS_ID -app-id cli_example -tenant feishu \
  -chat-id oc_qa_one -chat-id oc_qa_two -access allow -confirm
```

提交时会重新读取所选 App 的机器人所在群。任一所选群无法核实入群时，整批授权取消。授权即时生效，群 @ 规则及用户和路由权限分别沿用现有设置。

传入完整 `-chat-id` 且省略操作时，只读回该群的状态。未找到时 `groups` 为空。用 `joinedAvailable` 区分已读取所在群目录和目录暂不可用。

即时操作通过当前系统用户已登记的接收进程持久保存。配置文件、群授权和路由权限各自管理。接收端暂时无法列出所在群时，仍显示已有配置和运行覆盖。操作结果未确认时，先重新读取该账号状态再决定是否重试。 [群聊管理命令](RUNBOOK.zh-CN.md#会话命令)

四个入口分别展示飞书的全部群消息权限和线上版本是否订阅 `im.message.receive_v1`。CLI 输出 `messageScope`（`granted`、`missing` 或 `unknown`）和 `messageEvent`（`subscribed`、`missing`、`unpublished` 或 `unknown`）。旧接收端省略 `messageEvent`，界面显示「未知」。订阅状态只读，实际投递用一条新测试消息核对。缺少权限时，从飞书管理员私聊申请并复查。 [接收无 @ 的群消息](RUNBOOK.zh-CN.md#接收无--的群消息)

### 在线用户与路由权限

飞书管理员可在已授权私聊分页查看用户、管理员或指定路由的授权，并用一条 `/invite` 或 `/remove` 命令修改多个用户。变更与本机入口保存在同一份 App 运行策略中。 [飞书操作](RUNBOOK.zh-CN.md#查看和批量修改用户权限)

在 Web 或桌面的「运行状态」打开「在线用户与路由权限」，选择进程和 App。TUI 在当前账号按 `F12`。可以查看用户、管理员和逐路由权限的启动值、运行覆盖及本层生效值。编辑已有权限或填写本 App 的用户 Open ID，选择允许、撤销或恢复继承。

```sh
t3-bridge config live-access -process PROCESS_ID -app-id cli_example
t3-bridge config live-access -process PROCESS_ID -app-id cli_example -kind route -user-id ou_example -route-id work -mode deny
```

国际版 App 加 `-tenant lark`。权限保存在所选 App 的运行状态中，与飞书 `/invite`、`/remove` 共用事务和所有者保护。撤销用户或路由授权会拦截待派发任务，恢复授权后需要重新提交。实际派发仍会核对聊天范围、其他授权层和目标可用性。 [权限命令说明](RUNBOOK.zh-CN.md#会话命令)

### 在线运行设置

在 Web 或桌面的「运行状态」选择「在线运行设置」，按进程和 App 查看并调整生效值。TUI 在当前账号按 `F11`。支持并发预算、卡片进度、回复模式、工具活动、过程展示和默认空闲超时。

并发预算支持 `0` 至 `50`。`0` 表示 Bridge 不额外限制并发。调低预算后，在途轮次继续运行，新轮在有空位后派发。

回复模式可选 `card`、`text` 或 `markdown`。CLI 用 `-reply-mode markdown` 选择终态富文本，飞书用 `/config reply markdown`。回复样式按轮次保存，切换适用于之后的新回复。

过程展示可选 `off`、`brief`、`detailed`、`stream`、`stream-brief`、`stream-detailed` 或 `card`。组合模式在同一张独立过程卡上更新当轮已核验的公开助手正文和对应详细程度的工具状态。`card` 在开启卡片进度时把正文预览显示在原结果卡上，适用于卡片回复。CLI 用 `-process-mode stream-brief`，飞书用 `/config process stream-brief` 选择「正文＋简略工具」，从后续聊天轮次开始生效。 [各模式显示内容](RUNBOOK.zh-CN.md#会话命令)

```sh
t3-bridge config live-settings -process PROCESS_ID -app-id cli_example
t3-bridge config live-settings -process PROCESS_ID -app-id cli_example -max-concurrent 2 -progress=false
```

国际版 App 指定 `-tenant lark`。保存只更新明确修改的字段，与飞书 `/config` 读写同一份运行状态。回复模式、过程展示和默认空闲超时应用于后续轮次。结果未确认时先重新读取生效值。

在表单填写飞书应用、用户、聊天、T3 MCP 和路由。添加或移除路由后，同步调整默认和复核路由引用。展开「完整 JSON」可编辑高级字段。点「校验并查看差异」，通过后可下载候选 JSON。要写入 `-config` 指定的路径，使用对应的「配置文件」来源，再点「保存到配置文件」。关闭桌面窗口或在 Web 命令的终端按 Ctrl+C 可退出。

### 配置生效方式

| 生效方式 | 可调整内容 |
| --- | --- |
| 在线生效 | 并发预算、回复、进度、过程展示、空闲超时，以及用户、管理员、逐路由和群授权、群 @ 规则。通过运行页或 TUI 的 F9、F11、F12 保存各 App 的运行状态。回复样式等由后续轮次取值。 |
| 按 App 重连 | 使用已加载配置的原路径重读飞书密钥、T3 OAuth 凭据和 T3 运行文件，替换该 App 的连接。可以等待在途运行收口后重连。 |
| 重启实例 | 从磁盘读取新的 JSON，包括启用的 App、凭据引用路径、MCP 目标、路由、模型、执行权限、静态 ACL、功能开关和状态目录。已有在线覆盖继续优先。通过「接收就绪状态」核对运行进程加载的配置版本。 |
| 首次初始化 | `initialRuntime` 为新绑定的状态提供初值。已有状态从在线入口调整。 |

在「完整 JSON」选择另一个 JSON 文件，可用其内容替换当前草稿并预览差异。保存路径仍是启动时选定的配置文件，原选入文件可保留作迁移来源。

已有备份时，「从备份载入草稿」可预览上一次配置。核对差异后再保存，版本冲突时重新打开当前配置。

在 T3 MCP 填入回环 HTTP 或 HTTPS 端点，也可为本机使用运行文件。填写私有令牌文件后，在路由中展开「从 T3 查询项目与模型」。新模板可获取端点的环境 ID，已有配置会核对该环境。若要查询端点所关联的其它环境，先在路由填入该环境 ID。查询只读取项目信息和模型能力。选中项目与模型后可填入草稿，再核对执行权限、模型选项和用户授权。

「检查应用 API」向所选租户读取各账号的机器人身份。命令行可用 `config check-app -config PATH` 核对主账号，使用 `-app-id` 可选择附加账号。真实消息收发在独立接收实例启动后验收。

### 首次启动设置

账号的可选字段 `initialRuntime` 用于迁移和初始化接收状态。新接收端绑定 App 时应用其中的设置和群 @ 规则。已运行实例使用其持久化的运行设置，后续调整从「在线运行设置」和「在线群授权」保存。

在 Web、桌面和 TUI 的账号表单中审核或编辑「首次启动运行设置」。CLI 使用 `config patch`，主账号的路径为 `["initialRuntime"]`。例如可用以下对象设置回复模式、并发预算及指定群的 @ 规则。每个 `groupMentions` 键必须已列在同一账号的 `chats` 中。

```json
{
  "settings": {"replyMode": "markdown", "processMode": "stream", "maxConcurrent": 10},
  "requireMention": true,
  "groupMentions": {"oc_example": false}
}
```

### T3 MCP 首次授权

在环境尚为占位值的账号上，Web 或桌面可点「采用本机 T3」，TUI 在当前账号按 `T`，CLI 可生成相同的候选。

```sh
t3-bridge config local-t3 -config PATH > bridge.candidate.json
```

该操作读取 `T3CODE_HOME` 所指绝对目录或 `~/.t3` 下的 `userdata/server-runtime.json` 和同目录的 `environment-id`，填入当前账号连接及使用同一环境占位值的路由。候选经差异预览后保存。在 Web 或桌面修改完整 JSON 后，先校验草稿，再选择要配置的账号。自定义运行目录、已有明确环境及远程 HTTPS 端点可手填。凭据和实际环境身份在授权及预检时核对。 [T3 运行目录](https://github.com/pingdotgg/t3code/blob/main/docs/operations/observability.md#heap-snapshots) · [T3 路径定义](https://github.com/pingdotgg/t3code/blob/v0.1.10/apps/server/src/config.ts#L130-L164)

已有私有凭据文件时可继续引用该文件。首次授权时，在所选账号填入 T3 端点或本机运行文件，并将 `mcp.tokenFile` 指向用户可写的私有目录内一个绝对 `.json` 路径。内置通用模板预填用户配置目录中的 `credentials/t3-oauth.json`。运行 `t3-bridge config authorize-t3 -config PATH`，在同机浏览器打开终端显示的地址，按 T3 页面完成批准并选择执行权限。附加账号使用 `-app-id` 指定。

在 Web 和桌面工作台可展开「申请 T3 授权」。TUI 先选账号再按 `F6`。两处均使用当前草稿的连接和令牌路径。若窗口无法打开授权链接，可复制地址到本机浏览器。

T3 页面出现 **Sign-in code** 时，从同一 T3 环境的「Settings → Sharing & access → Use T3 Code in another app (MCP)」取得一次性登录码。也可以用该 T3 安装自带的 `t3 auth pairing create` 生成。把登录码填入 T3 授权页，按路由需要的权限批准。`approval-required` 路由可选择 **Supervised**。若 T3 提示登录码过期或已使用，从同一环境重新取码并填回。Bridge 最长等待批准 5 分钟。若 Bridge 提示等待超时，请重新准备授权并打开新的链接。 [T3 登录和访问权限](https://github.com/pingdotgg/t3code/blob/v0.1.10/docs/user/outside-agents.md)

批准后，命令将 OAuth 凭据写入该私有文件。重新授权并替换已存在的凭据需显式添加 `-replace`。可用 `config catalog` 查询项目、模型及权限上限。CLI 的授权和目录查询均可用 `-draft-stdin` 读取当前草稿。授权与查询完成后再启用接收。 [T3 外部应用授权说明](https://github.com/pingdotgg/t3code/blob/main/docs/user/outside-agents.md)

T3 授权响应提供范围时，Bridge 将它与令牌保存在同一个私有文件中。目录查询的 `operateAccess` 表示本端 OAuth 是否授予派发权限。`granted` 为已授予，`missing` 为未授予，`unknown` 表示凭据没有可核对的范围。TUI、Web 和桌面的目录显示相同状态。只读授权可查项目和模型。用它启用接收或派发任务时，Bridge 会提示重新选择至少监督权限。旧凭据没有范围时继续沿用原有核验流程。

HTTPS 直连使用系统信任的 TLS 证书。新签发的 HTTPS OAuth 文件绑定授权的 MCP 端点，切换端点时请授权到对应的私有文件。OAuth 回调仍使用本机浏览器。首轮原生附件上传要求同一端点来源。续聊附件本机缓存与本机工具身份核验仍要求接收端和 T3 同机。

首轮附件的准备、上传和派发沿用当前 MCP 会话的同一身份。通过私有文件轮换凭据后，接收端在重新建立并核验连接时采用新凭据。

### 使用 T3 关联环境

连接端和执行端都要在 T3 的「设置 → Labs」开启关联环境，再从连接端关联执行端。账号的 `mcp.endpoint` 或 `mcp.runtimeFile` 指向连接端，`mcp.environmentId` 填连接端 ID。路由的 `environmentId`、项目、provider 和模型填执行端的值。使用监督模式运行时，Bridge OAuth 和环境关联均需至少授予监督权限。 [T3 关联指南](https://github.com/pingdotgg/t3code/blob/v0.1.10/docs/user/remote-access.md#link-another-environment)

`config catalog -config PATH -route ROUTE` 可核对已保存路由的远端项目、模型和 `linkAccess`。TUI、Web 和桌面在项目与模型选择器同时显示运行模式上限和关联权限。只读关联可查项目、模型并复用已有目录。在关联环境新建项目还需要该关联授予 `full-access`。关联失效、权限不明或权限低于路由要求时，接收预检及实际派发均会提示检查关联。

### 从模型目录设置选项

Web 和桌面选中模型后，可用目录中的下拉框选择布尔值和字符串。TUI 在路由页按 F5 查询，选中带选项的模型后按 Enter 打开编辑，上下键选择字段，左右键选择值。选默认值会从配置中移除对应字段。确认先更新草稿，再走常规审阅保存。更换模型绑定会从空选项开始，同一绑定保留原有手填项。

CLI 命令 `t3-bridge config model-options -config PATH -route ROUTE < options.json > candidate.json` 用所选路由的 T3 目录校验一个 JSON 对象，输出完整候选配置。可用 `-app-id` 选择附加账号，也可用 `-profile` 选择已保存配置。对象替换该路由的全部选项，`{}` 使用模型默认值。继续用 `config diff` 检查，再用已取得的版本调用 `config save`。例如 T3 公布了布尔选项时，写 `false` 来选择布尔值。

### 登记已有目录为 T3 项目

先在草稿中填写并授权账号的 T3 连接，再明确路由的环境 ID。Web 和桌面支持从草稿登记。TUI 在当前路由按 `K`。核对 App、路由、目标环境及目录后再提交。第一次保存前也可完成登记，返回的项目字段会填入当前草稿。已保存配置继续支持从「登记 T3 项目」进入。

```sh
t3-bridge config register-project -config PATH -route work -directory "$WORKSPACE" > bridge.candidate.json
```

CLI 默认使用主账号及其默认路由。可用 `-app-id` 选择附加账号，`-expected-revision` 可核对已保存内容。该命令会向 T3 登记已有目录或复用精确匹配的项目，输出仅更新所选路由项目字段的候选。审核差异后仍需另行保存。目录须在目标环境已存在，登记权由 T3 凭据决定。若登记结果待核对，先在 T3 中查看该目录。 [T3 项目登记契约](https://github.com/pingdotgg/t3code/blob/v0.1.10/apps/server/src/mcp/toolkits/project/tools.ts#L76-L87)

从 CLI 草稿登记时加 `-draft-stdin`，并指定正式配置的保存版本。正式配置尚不存在时用 `missing`。可用 `config authorize-t3 -draft-stdin < "$DRAFT"` 申请运行凭据，也可用 `-token-file` 为登记准备单独授权。

```sh
t3-bridge config register-project -config "$CONFIG" -route work -managed \
  -draft-stdin -expected-revision missing < "$DRAFT" > "$CANDIDATE"
t3-bridge config diff -config "$DRAFT" < "$CANDIDATE"
t3-bridge config save -config "$CONFIG" -expected-revision missing < "$CANDIDATE"
```

新建项目需要当前 MCP 客户端具有 T3 `full-access`。监督或只读授权可读回并复用已有项目。可先在 T3 添加目录，或为本次登记提供一份单独授权的私有 OAuth JSON。Web 和桌面登记表单可填写该文件的本机绝对路径。TUI 在路由页按 `K` 后按 `F3` 填写路径。CLI 用 `-setup-token-file`。两种目录登记方式均支持此选项。

```sh
t3-bridge config authorize-t3 -config PATH -token-file "$SETUP_OAUTH_FILE"
t3-bridge config register-project -config PATH -route work -directory "$WORKSPACE" \
  -setup-token-file "$SETUP_OAUTH_FILE" > bridge.candidate.json
```

批准独立授权时，在 T3 页面选择 `full-access`。HTTPS 凭据须绑定当前配置连接的同一端点。候选配置沿用原运行凭据和路由模式，只更新项目字段。登记授权的有效期及撤销由 T3 管理。 [T3 项目写入权限](https://github.com/pingdotgg/t3code/blob/v0.1.10/apps/server/src/mcp/McpToolAccess.ts#L321-L353)

#### 准备本机专用工作区

同一台机器上的 T3 可使用「创建本机专用工作区」。Web 和桌面在登记项目页显示目录并要求确认。TUI 在路由登记界面提供同一选项。CLI 用 `-managed` 代替已有目录参数。

```sh
t3-bridge config register-project -config PATH -route work -managed > bridge.candidate.json
```

准备专用工作区前先绑定飞书应用或填写实际 App ID。确认后，在当前用户配置目录下的 `t3-bridge/workspaces` 准备私有目录，并在所选 T3 环境登记或复用对应项目。路径按配置文件、App 和路由固定，配置文件第一次保存前后使用同一个目录。登记失败后目录仍可检查和继续使用。专用目录独立于命名配置及接收状态的清除范围。项目字段随候选配置单独审阅和保存。直连远程和关联环境继续使用目标机上的已有目录。

### 扫码绑定

TUI 中按 `F7` 打开扫码绑定，扫码成功后按 Enter 保存私有凭据并更新草稿。终端窗口较小时可打开显示的授权链接。CLI 使用 `t3-bridge config bind-feishu -config PATH -output candidate.json`，扫码成功后把候选写入指定的新文件。`-comments` 同时申请云文档评论权限。核对差异后用各自的保存入口提交。

1. 展开「扫码绑定飞书应用」。如需云文档评论，勾选对应权限，再点「生成二维码」。
2. 用飞书或 Lark 扫码，或打开授权页，选择或创建 PersonalAgent 应用。
3. 点「保存本机凭据并更新草稿」。密钥存入用户配置目录下的 `t3-bridge/credentials/<appId>.secret`，权限为 `0600`。候选 JSON 记录文件路径。
4. 核对账号、T3 目标和路由授权。需要接收云文档评论时，填写评论路由 ID。保存后按应用预检并审阅接收启用草稿。 [按应用启用接收](#按应用启用接收)

扫码可填充模板身份。已有主账号时，扫描另一个 App ID 会新增账号草稿。新增账号复用主账号的 MCP 设置和默认路由指向的项目与模型，执行权限设为 `approval-required`，只给扫码人初始用户和路由权限。状态目录单独生成，接收授权初始关闭。

扫描已配置的主账号或附加账号时，按完整 App ID 更新该账号的 `appSecretFile`。飞书或 Lark 与配置不一致时，请在授权页核对应用。凭据文件已存在且密钥不同时，从「应用凭据」选择对应 App 并确认轮换，或在草稿中引用另一个私有文件。

### 扫码替换已有 App

更换 App ID 时，在 Web 或桌面的扫码区勾选「替换已有账号」，再选所需 App。TUI 选中该账号，按 `F7` 再按 `x`。CLI 用完整旧 App ID 指定替换范围。

```sh
t3-bridge config bind-feishu -config "$CONFIG" -replace-app-id "$OLD_APP_ID" -output replacement.json
```

扫码选择尚未出现在草稿中的应用。确认后只替换所选账号的位置。新账号使用独立状态目录，沿用所选账号的 T3 连接和默认路由项目，以 `approval-required` 运行，初始访问权限只授予扫码用户。旧用户、管理员、群规则及自动化配置留在旧配置中。原默认路由使用专用飞书工具时，换绑草稿保留项目，provider 和模型填入待选占位值。请在 T3 目录选择适合新 App 的目标。

审阅草稿前保留旧配置的私有副本。生成候选不会启动接收端，原凭据和状态留在原路径。先排空并停旧实例，再显式保存。新 App 初始关闭接收，需按 App 预检、审阅启用并启动。 [换绑和回退](RUNBOOK.zh-CN.md#接收账号换绑)

### 使用已有凭据文件替换 App

Web 和桌面打开「手动替换账号」，选择旧 App。TUI 选中旧账号后按 `F7` 再按 `m`。填入新 App 的完整 ID、租户、该应用下的初始用户 Open ID 和已有私有密钥文件的绝对路径。应用 ID 与 Open ID 以新应用为准。

```sh
umask 077
t3-bridge config replace-app -config "$CONFIG" -app-id "$OLD_APP_ID" \
  -new-app-id "$NEW_APP_ID" -tenant feishu -app-secret-file "$NEW_SECRET_FILE" \
  -user-id "$NEW_USER_ID" > replacement.json
```

换绑草稿沿用扫码替换的状态隔离和路由规则。只校验文件可作为私有密钥读取，继续引用原路径。需要核对应用身份时，显式运行「检查应用 API」或保存后的按 App 接收预检。再审阅差异、保存并单独启用接收。 [预检和启用](#按应用启用接收)

### 输入新 App Secret 换绑

已有新 App 的密钥时，可在本机工作台遮罩输入。Web 和桌面在「手动替换账号」留空文件路径，输入密钥后点击「保存新私有密钥并生成换绑草稿」。TUI 选中账号后按 `F7`、`m`、`F2`，填写新应用信息，按 `Ctrl+S` 提交。

提交后，密钥保存到用户配置目录下的 `t3-bridge/credentials/<appId>.secret`，再打开换绑差异。已有文件内容一致时复用。内容不同时提示核对，保留原文件。取消差异审阅时已保存的私有密钥可以在下次换绑继续使用。

CLI 使用 `-enter-secret`，交互终端以遮罩读取，也可从标准输入接收。候选 JSON 仍输出到标准输出。通过命名配置操作时可用 `-profile` 替代 `-config`。标准输入用于密钥时请从文件或 profile 读取配置。

```sh
umask 077
t3-bridge config replace-app -config "$CONFIG" -app-id "$OLD_APP_ID" \
  -new-app-id "$NEW_APP_ID" -tenant feishu -user-id "$NEW_USER_ID" \
  -enter-secret > replacement.json
```

先核对候选和新应用身份，再显式保存配置并按 App 预检。 [预检和启用](#按应用启用接收)

### 多个飞书账号

主账号使用顶层字段。额外账号放在 `additionalAccounts` 数组中，每项是一份完整的单账号配置。每个账号使用不同的 `appId` 和 `stateDir`，可共用同一个 T3 MCP 或使用各自的连接与路由。一个 Bridge 进程为每个 `receiverAuthorized: true` 的账号启动独立长连接，ACL、线程绑定、队列和回执分别保存。

### 按应用启用接收

可单独核对已保存的某个 App。Web 和桌面的「接收预检」可选择 App，TUI 在所选账号的总览页按 `A`。CLI 可运行 `config preflight -config PATH -app-id APP_ID -include-disabled`。`-app-id` 接受完整 App ID，`-include-disabled` 也会执行尚未启用账号的远端检查。CLI 报告的 `accounts` 只包含所选 App，`passed` 表示当前接收启动就绪，`allAccounts.passed` 表示所选范围通过包含未启用账号的预检。未启用的 App 可以通过核验，启用接收需另行保存。

保存应用凭据引用、T3 连接和路由后，从 Web 或桌面的「接收预检」进入按应用启用流程，或在 TUI 选中账号后按 `E`。预检只检查所选应用的飞书身份、T3 环境和路由，以及本机接收锁和状态目录锁。通过后可审阅将该应用的 `receiverAuthorized` 改为 `true` 的差异，并显式保存。其他 App 可各自核验。服务安装、启动和飞书首条消息在保存后分别进行。服务启动后，可按已保存配置核对各 App 的实际运行版本和连接。 [接收就绪状态](RUNBOOK.zh-CN.md#按配置核对接收就绪)

CLI 的 `config prepare-receiver` 将通过预检的候选输出为 JSON。配合源版本审阅并保存。

```sh
revision=$(t3-bridge config version -config "$CONFIG")
candidate=$(mktemp)
t3-bridge config prepare-receiver -config "$CONFIG" -app-id "$APP_ID" \
  -expected-revision "$revision" > "$candidate"
t3-bridge config diff -config "$CONFIG" < "$candidate"
```

确认差异且预检已通过后，运行：

```sh
t3-bridge config save -config "$CONFIG" -expected-revision "$revision" < "$candidate"
rm "$candidate"
```

工作台可通过扫码或「手动添加附加账号」将新应用加入草稿。手动添加时填写应用 ID、用户 Open ID、独立状态目录、凭据文件路径和初始路由。附加账号可分别调整应用、ACL、T3 MCP 和路由，也可从草稿移除。使用示例 systemd 单元时，各账号的状态目录可放在 `/var/lib/t3-bridge` 下。

## Agent 和 CLI

### 从草稿配置

首次配置可在私有草稿中完成飞书绑定、本机 T3 发现、T3 授权及目录查询，最后再写入正式配置。已准备好飞书应用时，可在草稿填入应用 ID、私有密钥文件路径和用户权限。远程 T3 连接可手填，跳过本机发现。

```sh
umask 077
t3-bridge config init -draft > bridge.template.json
t3-bridge config bind-feishu -draft-stdin -output bridge.bound.json < bridge.template.json
t3-bridge config local-t3 -draft-stdin < bridge.bound.json > bridge.local.json
t3-bridge config authorize-t3 -draft-stdin < bridge.local.json
t3-bridge config catalog -draft-stdin -route work < bridge.local.json
```

`init -draft` 和 `local-t3` 将候选写到 stdout。扫码绑定将候选写入 `-output` 指定的新私有文件。授权按草稿设置写入独立的 OAuth 文件。绑定、发现、授权、查询和校验的 `-draft-stdin` 不能与 `-config` 或 `-profile` 同时使用。项目登记用 `-config` 指定最终写入路径，规则见[项目登记](#登记已有目录为-t3-项目)。选定项目、执行器、模型及权限后，用 `config check -draft-stdin` 校验，再单独保存、预检并启用接收。

### 修改已有配置

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

`config patch` 从 stdin 接收一组字段修改，先全部应用，再校验整份配置，成功时把候选 JSON 写到 stdout。路径元素使用完整对象键，例如 `"Work.Team"`。附加账号使用从零开始的下标字符串，例如 `["additionalAccounts","0","commentRoute"]`。可用 `{"path":["reviewRoute"],"delete":true}` 删除可选字段。`config export` 输出归一化、经过校验的 JSON。两条命令都以源文件作为输入，重定向时使用单独的候选路径。

### 同线程续聊附件

本机 T3 路由可开启 `cacheFollowupAttachments`。在 Web 或桌面的路由表单勾选「续聊附件通过本机缓存排队」，在 TUI 编辑该路由字段，或用 CLI `config patch` 设置 `{"path":["routes","work","cacheFollowupAttachments"],"value":true}`，其中 `work` 为路由名。

开启前需要确认 T3 执行器能读取该接收账号的 `stateDir/followup-attachments` 私有目录。续聊把图片和文件保存在按请求隔离的目录内，由原 T3 线程通过 MCP `queue` 接收带路径的独立轮次。从缓存写入起超过 24 小时且已结清的目录可清理，在途和待核对请求继续保留。保存配置并重启后，已有会话也采用当前路由设置。

同环境的首轮附件走 T3 原生启动，包括同环境 HTTPS 直连。已有会话需要本机缓存队列才能接收新附件。缓存关闭或无法使用时，飞书会提示发送 `/new`，然后在新消息中重发附件。关联环境的附件需要为目标环境使用可上传的直连配置。 [T3 附件发送契约](https://github.com/pingdotgg/t3code/blob/v0.1.10/apps/server/src/mcp/toolkits/attachment/handlers.ts#L93-L110)

| 附件提交方式 | 每轮数量 | 单张图片 | 单个普通文件 | 每轮总量 |
| --- | ---: | ---: | ---: | --- |
| T3 MCP 原生首轮 | 8 项 | 10 MiB | 50 MiB | 附件合计 100 MiB，其中图片合计 80 MiB |
| 本机缓存续聊 | 10 项 | 25 MiB | 50 MiB | 附件合计 100 MiB |

同轮引用、转发及合并输入的附件计入上限。原生路径按内容识别图片，即使图片作为文件发送，也计入 80 MiB 的图片总量。超限会提示分批发送或报告单项上限，该轮不会提交模型。 [T3 首轮契约](https://github.com/pingdotgg/t3code/blob/v0.1.10/apps/server/src/mcp/toolkits/project/tools.ts) · [T3 续聊契约](https://github.com/pingdotgg/t3code/blob/v0.1.10/apps/server/src/mcp/toolkits/attachment/tools.ts) · [T3 体验层附件限制](https://github.com/pingdotgg/t3code/blob/v0.1.10/packages/contracts/src/chatAttachment.ts)

`config init` 只允许创建新文件。`config save` 要求使用读取源文件时取得的版本。成功覆盖时会将原配置保存为 `<文件名>.backup.json`。`config backup` 可读取、校验并输出这份备份。发现版本冲突后，重新读取文件、应用修改并核对差异。`config check` 只做本地结构校验，`config catalog` 可按账号调用已授权的 T3 MCP，读取真实项目和模型能力。

### 编辑工具身份绑定

Web 和桌面的路由表单展开「飞书工具」，选择模式、填好 CLI、配置目录、profile 和对应的 Open ID，再点「应用到草稿」。TUI 在路由页按 `B` 打开同一组字段。两个入口均保留原始 `toolContext` JSON 编辑。应用草稿后可预览完整配置的差异，再显式保存。选择关闭会从该路由草稿中移除工具配置。

CLI `config tool-binding` 接受同样的字段并输出候选 JSON。省略 `-app-id` 时选择主账号，工具 App 和租户默认沿用该接收账号。以下命令创建应用身份的候选配置。

```sh
t3-bridge config tool-binding -profile work -route work -mode bot \
  -cli-path "$LARK_CLI" -cli-config-dir "$LARK_CONFIG_DIR" \
  -cli-profile dedicated-bot -bot-open-id "$BOT_OPEN_ID" > work.candidate.json
```

固定用户使用 `-mode user -user-open-id`，双身份使用 `-mode user-default` 并提供两个 Open ID。关闭用 `-mode off`。候选仍需通过路由的应用、用户和环境校验。保存后用独立的工具身份核验入口检查实际授权。

### 专用 bot 工具身份

在路由的 `toolContext` 中配置 `botBinding`，可以在新派发和续接前核对本机工具身份。此模式用于本机 T3 环境中的专用 provider。使用同一 provider 的路由须配置同一绑定。将 T3 provider 配置为使用该 CLI 的私有配置目录和 profile，并让 Bridge 与专用执行端使用对应的系统用户。填写从工具 App 独立取得的机器人 Open ID。

```json
{
  "cliPath": "/private/tools/lark-cli",
  "environmentId": "LOCAL_T3_ENVIRONMENT_ID",
  "identity": "bot",
  "botBinding": {
    "appId": "cli_example",
    "botOpenId": "ou_example_bot",
    "tenant": "feishu",
    "profile": "dedicated-bot",
    "configDir": "/private/bridge/tool-app"
  }
}
```

以接收端的系统用户执行 `t3-bridge config check-tool-identity -config config/local.json -route work`。命令核对严格 bot 模式、实际机器人 Open ID 和列群权限，输出核验结果。附加账号可用 `-app-id` 选择。 [绑定编辑](#编辑工具身份绑定)

此核验确认接收主机上的 CLI 身份。T3 的逐轮凭据隔离仍依赖专用 provider 和执行账号的配置。更换绑定会开启新的 Bridge 会话上下文，在途请求按原回执追踪。开启绑定前保留上一版可读的配置供回退使用。

### 专用用户工具身份

个人路由可以用 `identity: "user"` 和 `userBinding` 使用已有的飞书用户授权。沿用上述 CLI 路径、环境和专用目录。`userBinding` 包含 `appId`、`tenant`、`userOpenId`、`profile` 和 `configDir`。应用及租户须与该接收账号一致。把路由的 `users` 配置为只包含同一个 `userOpenId`。为个人凭据配置独立 provider，同一 provider 的路由须共用该绑定。

在私有 CLI profile 中由用户完成 OAuth，并配置严格 `user` 模式。`config check-tool-identity` 会核对本机 profile 和飞书服务端返回的用户 Open ID。新任务和续聊都核验同一授权，只接受该用户发给本 App 的私聊。群消息、会议、评论和自动调查可继续使用现有 bot 路由。应用或身份不符时会拒绝派发。配置编辑沿用 CLI、TUI、Web 和桌面的 `toolContext` 入口。

旧候选版会拒绝 `userBinding`。回退时使用保存的旧版配置及其原 provider。

### 专用应用与用户双身份

需要在个人私聊中同时使用应用工具和已授权的个人工具时，将路由的 `toolContext.identity` 设为 `user-default`，同时填入 `botBinding` 和 `userBinding`。两个绑定的 `appId`、`tenant`、`profile` 和 `configDir` 一致，应用及租户与接收账号相同。`botOpenId` 和 `userOpenId` 分别使用机器人和个人的实际 Open ID。路由的 `users` 只填这个用户，其他聊天和自动任务可使用独立的 bot 路由。

```json
{
  "cliPath": "/private/tools/lark-cli",
  "environmentId": "LOCAL_T3_ENVIRONMENT_ID",
  "identity": "user-default",
  "botBinding": {
    "appId": "cli_example",
    "botOpenId": "ou_example_bot",
    "tenant": "feishu",
    "profile": "personal",
    "configDir": "/private/bridge/personal-tools"
  },
  "userBinding": {
    "appId": "cli_example",
    "userOpenId": "ou_example_user",
    "tenant": "feishu",
    "profile": "personal",
    "configDir": "/private/bridge/personal-tools"
  }
}
```

让专用 provider 使用同一 CLI 配置和 profile。此 profile 需要已有用户 OAuth，以及旧桥双身份预设对应的 `strict-mode: off` 和 `default-as: auto`。Bridge 在派发前查询现有策略，通过两个显式身份核对机器人、列群权限和用户 Open ID。执行端用 `--as user` 访问个人资源，用 `--as bot` 代表应用操作。任一身份核验失败时该轮不派发。 [CLI 严格模式](https://github.com/larksuite/cli/blob/v1.0.96/cmd/config/strict_mode.go) · [CLI 默认身份](https://github.com/larksuite/cli/blob/v1.0.96/cmd/config/default_as.go)

四个入口使用相同的路由 JSON。保存后可用 `config check-tool-identity` 核验本机身份。在 TUI 打开所选账号的路由，按 `V` 核验。Web 和桌面从「核验飞书工具身份」选定已保存的私有配置、账号和路由，再执行检查。Web 核验需要用终端打印的完整启动地址建立会话，桌面会自动完成。界面结果对应一次本机 CLI 检查，运行时可从飞书 `/status` 查看当前路由类型。

#### 在飞书选择已配置的工具身份路由

为同一工作项目配置不同工具身份的本机路由，分别使用专用 provider 实例，并为使用者配置两条路由的授权。两条路由的实际环境、项目、运行权限和交互模式须一致。个人工具仍只允许绑定的用户在自己的已授权私聊使用。

所有者或管理员先进入其中一条路由，再直接发 `/config identity` 查看当前身份及可选路由。选项显示目标路由的工具身份和模型。可在卡片确认，也可直接发 `/config identity use <routeId>`。当前私聊中的运行及等待输入结清后可切换。所选路由成为该私聊的默认路由。显式指定路由、引用回复和已启用的分类器沿用现有分派规则。切回时可继续原路由的会话，派发和续接时仍会重新检查实际身份与权限。

更换身份模式会开启新的会话上下文。在途任务继续按原回执跟踪。回退到不支持 `user-default` 的版本前，先排空并准备上一版可读的配置和对应 provider。

### 维护本机 App Secret

凭据维护以已保存的配置为准。按 App 查看 `appSecretFile` 的路径和状态。可以写入或轮换私有文件，也可确认删除。要维护的文件须为私有目录中的普通文件。同一份配置中供多个 App 或其他凭据用途共用的文件，以及符号链接，应先在配置中改用独立引用。

```sh
t3-bridge config credentials list -profile work
t3-bridge secrets set -profile work -app-id cli_example < "$NEW_APP_SECRET_FILE"
t3-bridge secrets remove -profile work -app-id cli_example -confirm
```

在交互终端直接运行 `secrets set` 会隐藏输入回显。脚本可从私有文件重定向 stdin。

给程序读取时，可在明确指定 `-config` 或 `-profile` 后调用旧版 v1 协议。请求从 stdin 发送，响应中 `values` 仅包含查询成功的凭据值，未知或不可读取的条目进入 `errors`。普通凭据列表展示路径与状态。

```sh
printf '%s\n' '{"protocolVersion":1,"provider":"t3-bridge","ids":["app-cli_example"]}' |
  t3-bridge secrets get -profile work -app-id cli_example
```

Web 和桌面从配置工作台打开「应用凭据」，选定已保存配置和 App 后进入设置或删除确认。TUI 在浏览页按 `C` 打开凭据列表，按 Enter 隐藏输入，按 `p` 从私有文件导入，或按 `d` 进入删除确认。更新与删除作用于确认页显示的本机文件。多个配置引用同一文件时，后续读取共享该文件的结果。

接收端建立飞书 Channel 时读取凭据。更新后可先核对新凭据，再按运行指南重启接收端。飞书端 App Secret 的吊销在开放平台操作。 [受管服务操作](RUNBOOK.zh-CN.md#用户级后台服务)

### 从 lark-channel-bridge 导入配置

四个配置入口均可导入 V2 命名配置，以及根层含 `accounts.app` 或 `app` 的早期单账号 JSON。可从普通单路由模板开始。在 Web 或桌面展开「导入旧 lark-channel-bridge 配置」。在 TUI 按 `L`，用 Tab 切换输入框，按 F2 清空当前字段，按 Ctrl+S 预览。核对预填的旧配置路径，再填写新状态目录和私有密钥文件的绝对路径。单账号格式的旧 profile 留空。V2 留空会使用旧文件的 `activeProfile`。核对草稿和差异后，可按原流程保存或导出。启用接收前请选定并核对实际的 T3 目标。

检测到的路径可编辑，配置导入和旧私聊转接共用路径查找。若设置了 `LARK_CHANNEL_HOME`，在该绝对目录查找 `config.json`。未设置时先查 `~/.lark-channel/config.json`，缺失时再查早期的 `$XDG_CONFIG_HOME/lark-channel-bridge/config.json`，其中 `XDG_CONFIG_HOME` 缺省为 `~/.config`。检测只检查文件元数据。自定义安装可填写旧 JSON 的本机绝对路径。

在 CLI 使用 `config import-legacy`。`-config` 可指定已有底稿，省略时使用普通单路由模板。`-profile` 指定要导入的旧 profile。省略 `-legacy-config` 时使用检测到的来源，显式传入该参数可选择其他文件。

```sh
t3-bridge config import-legacy \
  -state-dir "$NEW_STATE_DIR" -app-secret-file "$APP_SECRET_FILE" > bridge.candidate.json
```

候选的主账号使用旧应用 ID、租户、用户、管理员和允许群。显式配置的 T3 目标及逐路由权限保持原样。普通单路由模板中的默认路由仍要求审批，只把它的用户占位值替换为候选主账号的明确允许名单。多路由、已绑定 App 或配置了专用能力的底稿须明确填写逐路由 Open ID。V2 profile 必须明确列出至少一个允许用户。早期单账号文件在 `preferences.access` 未列出用户字段时，沿用底稿主账号中明确填写的 Open ID。允许群只约束群聊，私聊继续依照用户与路由授权。密钥通过私有文件引用。候选关闭所有账号的接收授权，主账号状态目录使用尚不存在的独立路径。旧会话、任务回执和运行时所有者需分别核对。

导入预览还会列出首次启动的回复、工具活动、过程展示、并发、空闲超时及群 @ 规则。未开启独立过程消息的旧卡片回复使用本卡正文预览，旧流式 Markdown 使用终态 Markdown 和独立正文预览。已开启的独立过程简略或详细设置分别迁移为 `stream-brief` 或 `stream-detailed`。旧桥有效的 `1` 至 `50` 并发值按原值导入。逐群 @ 覆盖只迁移已在允许名单中的群。保存并启用前，可在草稿里复核和调整这些值。 [首次启动设置](#首次启动设置)

从 V2 T3 profile 迁移时，可在 CLI 加上 `-reuse-t3`，在 TUI 导入表单按 Ctrl+T，或在 Web 和桌面勾选「沿用旧 T3 连接和 OAuth 文件路径」。单默认路由草稿的环境尚为占位值时，导入会更新回环 HTTP 或 HTTPS 端点，或本机运行文件引用，同时沿用 OAuth JSON 文件路径和环境 ID。默认路由使用同一环境。预览只处理配置引用。保存后用能力目录读取并选择项目、provider 和模型，并核对连接身份与权限。 [T3 MCP 首次授权与能力查询](#t3-mcp-首次授权)

沿用本机运行文件或回环端点时，迁移预览会为默认路由开启本机续聊附件缓存，草稿中显式填写的开关优先。启用接收前请确认 T3 执行器能读取新状态目录下的私有缓存。 [同线程续聊附件](#同线程续聊附件)

沿用旧连接时，尚未选择项目或填写目录的默认路由也会带入旧 profile 的绝对默认工作目录。目录使用目标机路径格式，支持 Unix 绝对路径、Windows 绝对盘符路径和 UNC 路径。查询同一 T3 环境的能力目录后，目录唯一匹配一个可用项目时，工作台会预选该项目。已有项目 ID 优先，查不到或匹配多个时由操作者选择。CLI 可用 `config catalog -config PATH -route work` 获取指定路由的 `suggestedProjectId`。项目和模型仍通过现有预览与保存流程确认。

单账号 JSON 直接保存了 App Secret 时，CLI 可追加 `-copy-app-secret` 将该值复制到所选私有文件。TUI、Web 和桌面也能在确认后复制，无需安装旧桥。若旧文件使用环境变量、文件或外部命令引用，使用普通导入流程填写准备好的私有凭据文件路径。

旧桥把 App Secret 保存到加密库时，可在上述命令追加 `-legacy-bridge "$OLD_BRIDGE_BINARY"`，填写已安装旧桥的绝对路径。选择旧桥使用的原配置文件。命令调用其 `secrets get` 协议读取所选 profile 的 App Secret，将它保存到 `-app-secret-file` 指定的私有文件。候选 JSON 只写入路径。

TUI、Web 和桌面从旧加密库复制时，工作台从本机 PATH 查找 `lark-channel-bridge`，也可在启动 `config tui`、`config web` 或 `config gui` 时传入 `-legacy-bridge "$OLD_BRIDGE_BINARY"`。Windows 的 npm 安装可使用标准启动器或包内的 `.mjs` 入口，由本机 `node.exe` 运行。选择旧配置和 profile，填入新凭据文件路径，然后预览并核对 App、profile 和目标路径。确认复制后，再按常规流程保存或导出候选 JSON。

在 TUI 中按 `L` 填写导入表单，按 Ctrl+S 预览，再按 `c` 查看凭据复制确认页，按 `y` 复制。

在 Web 或桌面的导入表单中选择「预览导入与复制 App Secret」，检查差异后点击「确认复制 App Secret」。取消复制会保留导入草稿。

复制只读取所选 App 的密钥。请将目标文件放在当前用户的私有目录，目录缺失时会自动创建。目标文件已含相同凭据时可重复执行。已有不同凭据时保留原文件并提示冲突。使用现有私有凭据文件时，按普通导入流程填写其路径即可。

### 迁移旧命名工作区

CLI、TUI、Web 和桌面均可迁移旧版 `workspaces.json` 的 `named` 对象。Web 和桌面打开「迁移旧命名工作区」，选择已保存的目标配置和 App。TUI 在目标账号按 `W` 进入来源和映射编辑，按 Ctrl+S 预览。来源留空时，用目标 App 和租户匹配旧单账号配置或唯一的旧 V2 profile。也可填写本机旧文件的绝对路径。预览显示实际来源、将新增及保留的工作区，并统计未映射数量。 [本机旧配置查找规则](#从-lark-channel-bridge-导入配置)

Web 和桌面点「查看旧别名」，从来源列表选择别名，填写目标用户、聊天、话题及路由，再点「追加映射到草稿」。已有条目保留在同一个 JSON 数组中。预览前也可直接编辑数组。

TUI 在来源或映射页按 `F3` 查看旧别名，用方向键浏览并按 Enter 选择。填写目标字段后按 Ctrl+S 加入草稿。回到映射编辑页后，再按 Ctrl+S 校验并预览完整迁移计划。

映射逐条指定源键、目标用户、聊天类型、聊天或话题和路由。源键用旧文件中的完整键。旧别名按 profile、所有者和作用域存储时，键内的分隔符用 JSON 的 `\u001f` 表示。

```json
[
  {
    "sourceKey": "dev\u001fowner\u001foc_example\u001fInbox",
    "userId": "ou_example",
    "chatId": "oc_example",
    "chatType": "p2p",
    "routeId": "work"
  }
]
```

目标配置必须在所选路由的 T3 环境中声明项目 ID 和与旧目录匹配的 `projectPath`。目录唯一匹配一个项目时可省略 `projectId`。有多个匹配时，映射须填写具体项目 ID。话题映射还需填写 `topicId`。先预览并停掉目标接收端，再点「确认写入」、在 TUI 按 `s`，或在 CLI 重复预览参数并追加 `-confirm "$REVIEW"`。确认会核对已审阅的来源、映射、目标配置和迁移结果。TUI 写入后显示来源文件，按 Esc 返回配置。遇到同名且不同项目的冲突时，整批保持原状态。

```sh
t3-bridge config import-workspaces -config bridge.json -app-id cli_example \
  -mapping workspace-map.json
```

CLI 可用 `-legacy-workspaces workspaces.json` 指定来源。显式来源支持原有的 `-apply` 直接写入。使用自动发现时，以预览结果中的 `review` 确认写入。

需要查找完整源键时，可在 CLI 使用 `-list`。JSON 结果包含实际旧文件、别名的 `sourceKey` 和目录、可选普通路由及无效条目数。列表可直接用于编写映射，话题别名会标记为需要目标 `topicId`。

```sh
t3-bridge config import-workspaces -config bridge.json -app-id cli_example -list
```

## 字段

- `receiverMachine` 是实例标识，`receiverAuthorized` 控制是否允许启动接收端。`stateDir` 指向此实例专用的绝对路径。
- `appId` 和 `appSecretFile` 对应飞书应用，`tenant` 默认为 `feishu`，国际版填写 `lark`。`users`、`admins`、`chats`、`topics` 限定权限范围。用 `directChats` 可单独指定允许的私聊 ID，空数组允许经用户和路由授权的私聊。省略 `directChats` 时，私聊继续沿用原有 `chats` 范围。秘密文件使用绝对路径且仅限运行用户访问。
- `mcp.endpoint` 指向端点的 `/mcp`，支持回环 HTTP 或 HTTPS。T3 桌面端也可用 `mcp.runtimeFile` 指向其绝对路径下的 `server-runtime.json`，Bridge 在建连时读取端口并使用 `127.0.0.1`。`mcp.endpoint` 和 `mcp.runtimeFile` 二选一。`mcp.environmentId` 必须与 MCP 端点读回的环境一致。`mcp.tokenFile` 接受私有的纯令牌文件或包含 `access_token` 的 OAuth JSON。
- `routes` 以 `routeId` 为键。每个目标显式填写 `environmentId`、`projectId`、`driver`、`providerId`、`model`、`options`、`runtimeMode`、`interactionMode` 和用户范围。T3 必须已能访问指定环境与项目。`hostName` 和 `aliases` 为可选匹配名，重复、冲突或与保留命令同名会被拒绝。
- `defaultRoute` 引用已配置的普通路由。`reviewRoute` 只经 `/review` 使用，不能同时设为默认路由。路由仍受调用人权限、目标可用性和实际 T3 能力约束。
- `commentRoute` 启用云文档评论并指定执行路由。评论者必须同时通过该账号的用户授权和该路由的授权。评论回复对文档读者可见，可选择单独的项目与执行权限。
- `classifier` 可选。支持配置经验证的官方 Hy4 接口及私有密钥文件。未配置时，普通消息可使用已授权的默认路由。未知或冲突目标会要求明确选择。已配置分类器报错时保留错误处理边界。

严格校验拒绝未知字段、错误的固定字段大小写、重复键、类型错误和尾随文档。动态 routeId、node/check 和机型选项的大小写与点号原样保留。配置文件上限 1 MiB。运行实例在启动时读取配置，飞书中的运行设置和动态 ACL 另存于实例状态。
