# 运行指南

[English](RUNBOOK.md) | 简体中文

## 构建发布包

在仓库中使用 Go 1.25 或以上和 `zip` 构建。传入的版本会写入二进制。脚本输出 ZIP 的 SHA-256 和路径，解压后用二进制的 `--version` 核对版号。

```sh
sh deploy/build-release.sh v0.1.0 bin/t3-bridge-mac.zip desktop
GOOS=linux GOARCH=amd64 sh deploy/build-release.sh v0.1.0 bin/t3-bridge-linux-amd64.zip core
GOOS=windows GOARCH=amd64 sh deploy/build-release.sh v0.1.0 bin/t3-bridge-windows-amd64.zip desktop
```

第一条示例在 macOS 上运行。`core` 提供 CLI、TUI 和 Web，`desktop` 还可用 `config gui` 打开原生配置窗口。macOS 桌面构建需要本机工具链。Linux 桌面构建需要在 Linux 中安装 GTK 3 和 WebKitGTK 4.1 开发包及 `pkg-config`。运行桌面包需要对应运行库，解压后可执行 `sh "./T3 Bridge Config.sh"`。Windows 桌面运行需要 WebView2，解压后可双击 `T3 Bridge Config.cmd`。ZIP 包含二进制、配置示例及使用文档。

macOS 可另建直接打开配置的应用包。桌面 ZIP 和 `.app` 的原生对象都按 macOS 13.0 编译和链接，在同一台构建机切换部署目标时会使用对应的 Go 构建缓存。构建脚本对完整 `.app` 做本机临时签名。公开分发仍需单独办理 Developer ID 签名及公证。设置同一版本即可在运行页核对工作台与接收进程。

```sh
T3_BRIDGE_BUILD_VERSION=v0.1.0 sh deploy/build-macos-config.sh 'bin/T3 Bridge Config.app'
```

ZIP 根目录和 macOS 应用的 `Contents/Resources` 包含项目许可证及第三方声明。更新 Go 模块、发布用 Go 工具链或随包分发内容时，按发布目标核对依赖和声明。 [项目许可证](../LICENSE) · [第三方声明](../THIRD_PARTY_NOTICES.md)

## 启动和部署

1. 在 T3 配置单独的 MCP 访问身份，核对本机环境及每条路由的项目、provider 和模型能力。准备具备消息接收及回复权限的飞书应用，开启长连接接收。
2. 用部署用户可读的私有文件保存凭据，填写 JSON 配置并校验。核对应用身份和权限后，审阅并保存接收启用草稿。 [按应用启用接收](CONFIGURATION.zh-CN.md#按应用启用接收)
3. 独立测试实例先核对私聊收发、群授权、续聊和停止。使用 `/doctor` 查看连接的最近核验结果，使用 `/doctor --echo <routeId>` 核对专用 T3 目标是否返回 `OK`。
4. Linux 可参考 [systemd 单元](../deploy/t3-bridge.service)。该单元使用专用账号 `t3-bridge`、`/etc/t3-bridge/config.json` 和 `/var/lib/t3-bridge`。安装前核对实际路径与最小权限。

同一飞书应用只运行一个接收实例。不同实例使用各自的应用、凭据和状态目录。切换已有实例先暂停输入、排空任务及通知，再停止旧接收端。保留最新状态，停机后用 `-drain-check` 核对。部署和回退都保留持久去重、授权、停止及待核对身份。

同一用户配置目录下，接收进程按租户和 App ID 加锁，防止多个命名配置同时处理同一应用。使用自定义 systemd 单元的专用用户需要可写的配置目录，仓库样例通过 `XDG_CONFIG_HOME` 将锁保存在服务状态目录。跨用户或跨主机切换仍按停旧再启新的顺序运行。

macOS 可用用户级 LaunchAgent 启动。配置 `mcp.runtimeFile` 后，每次建立 MCP 连接时都会读取 T3 桌面端的当前端口。替换使用同一飞书应用的 LaunchAgent 时，等待旧任务卸载及进程退出后再启动新任务，同时禁用旧任务的登录自启。检查新进程、T3 环境身份和飞书 WebSocket 就绪日志后再结束切换。

多 App ID 共用配置时，`-drain-check` 和其他离线状态命令默认操作主账号。附加账号使用 `-app-id <App ID>` 指定。进程退出或重启会收口它管理的全部长连接。

### 用户级后台服务

`service` 可管理 macOS LaunchAgent、Linux systemd 用户服务和 Windows Task Scheduler，使用显式实例名。`render` 只输出服务定义。`install` 写入定义，`start` 启动实例并开启登录自启。

```sh
t3-bridge service render -instance work -config /private/bridge/work.json
t3-bridge service install -instance work -config /private/bridge/work.json
t3-bridge service start -instance work
t3-bridge service status -instance work
t3-bridge service list
t3-bridge service logs -instance work -lines 100
```

`render` 和 `install` 也可用 `-profile` 指定命名配置。安装后按实例名称管理。`restart` 重启指定实例，`stop` 停止实例并关闭登录自启。`unregister` 移除服务定义，配置、凭据、运行状态和日志仍可用于回退。`list` 显示本用户的受管实例。`logs` 在 macOS 和 Windows 读取该实例的 stdout 和 stderr。Linux 按当前 UID 和受管用户单元查询可读 journal，兼容日志存入系统 journal 的环境。 [journalctl 查询规则](https://manpages.debian.org/bookworm/systemd/journalctl.1.en.html)

Web 和桌面工作台的「运行状态」页显示已登记的受管实例、状态、可用 PID 及当前用户的接收进程。可以在页面启动、停止、重启或注销已确认归属的受管实例。操作后刷新实例清单。停止、重启和注销作用于整个实例，一个进程承载多个应用时标记「多 App 共享」。

在 TUI 按 `M` 查看已登记的受管实例。方向键选择，`l` 查看日志，`s` 启动，`x` 停止，`R` 重启，`u` 注销。在确认页按 `y` 提交后刷新状态。在 Web 和桌面的运行页可点实例旁的「查看日志」。这些界面和 `service logs` 共用平台读取逻辑，界面显示整个实例的日志，每个输出流最多 100 行。

在工作台选择注销后，核对实例名和影响范围，再确认移除服务定义。注销会停止整个实例及其所有 App 的接收，T3 已提交的工作可能继续。配置、凭据、运行状态和日志保留。需要恢复接收时，用原配置重新安装服务并启动。

在工作台保存配置后，可打开「接收预检」，继续检查或安装后台服务。也可从「运行状态」打开安装页，再选已保存的配置。核对配置路径、接收程序路径和实例名，提交安装后回到运行页启动。服务定义会引用安装时的程序路径。使用 macOS 独立配置应用时，先将应用放在长期使用的位置。

在 TUI 打开已保存的配置，按 `I` 输入实例名，按 Enter 预览，按 `y` 安装。安装完成后按 `s` 启动并开启登录自启，或按 Esc 返回配置。

安装前可在 Web 或桌面点击「检查启动条件」，或在 TUI 安装确认页按 `p`。终端可运行 `t3-bridge config preflight -profile work`，也可用 `-config` 指定已保存的私有 JSON。默认使用当前密钥核对已启用账号的飞书应用身份，并用账号的 MCP 凭据核对 T3 环境和各条路由。

在启用接收前，Web 和桌面可从「接收预检」选择「检查全部账号（含未启用）」，TUI 可从配置页或安装确认页按 `P`，CLI 可在原命令追加 `-include-disabled`。也可在 Web 或桌面选择某个 App，在 TUI 账号总览按 `A`，或在 CLI 加上 `-app-id APP_ID`。CLI 的 `passed` 表示检查范围内已启用账号的启动预检结果，`allAccounts.passed` 表示所选范围包含未启用账号时的核验结果。所选检查通过时退出码为零。预检完成后，在运行状态页和飞书测试会话核对实际长连接和首条消息。

展开接收进程下的「连接与任务」，可逐个查看 App 的飞书 WebSocket 状态、T3 最近建连时的环境核验，以及入站、在途、待核对和送达缺口。超过 30 秒或尚未采样的状态显示未知。送达缺口按回复轮次统计，包含已记录的通知异常和尚无通知标记的终态回复。

在 TUI 账号总览按 `Ctrl+D` 查看当前 App，按 `r` 读取最新快照。CLI 用 `ps` 找到完整进程 ID 后查询同一份快照。

```sh
t3-bridge config receiver-status -process PROCESS_ID -app-id APP_ID -tenant feishu
```

CLI 的 `sample` 包含观察时间 `at`、诊断字段 `diagnostics` 和可用的事件 `events`。快照未知时返回 `"sample":null`。进程或账号不匹配时命令报错。四个入口均读取本机快照，WebSocket 状态以显示的观察时间为准。

共享进程中的 App 等待自动重试或已耗尽重试时，工作台显示已失败尝试数和可用的故障类别。CLI 返回 `"sample":null` 和 `worker`。其中 `state` 为 `retrying` 或 `exhausted`。前者含下次尝试计划时间 `retryAt`。在工作台刷新可读到新的状态。重试耗尽后，可检查该 App 的故障，再从同一进程对它发起立即重连。

飞书长连接断开时，可在本机运行页的账号下选择「重连此 App…」，或在 TUI 账号总览按 `Ctrl+R` 并确认。CLI 用 `t3-bridge ps` 找到完整进程 ID，再运行 `t3-bridge config live-reconnect -process PROCESS_ID -app-id APP_ID -tenant feishu -confirm`。Lark 账号使用 `-tenant lark`。

新版接收端还可在同一入口选择等待在途 T3 工作收口后重连，或取消等待。CLI 分别附加 `-action wait` 和 `-action cancel`，仍需 `-confirm`。等待只暂停该 App 的新派发，在途任务及新入站回执继续保留。进程重启后仍会核对原轮次。收口后可恢复连接，即使飞书终态回复还需重试。运行页、`config receiver-status` 和 TUI 的 `Ctrl+D` 可核对是否正在等待。

重连替换所选 App 的飞书与 T3 MCP 连接，并沿用在途轮次和原回执。CLI 的 `started` 表示 T3 环境已核对，飞书正在建连。查询该 App 的最新快照以核对 WebSocket 就绪。`scheduled` 表示已登记等待，`pending` 表示沿用该 App 已有的等待。取消等待时，`cancelled` 表示已经取消，`idle` 表示没有等待中的请求。`unconfirmed` 或本机回执超时时，先查看运行状态再决定是否重新提交。

新版共享接收进程中，若某个 App 已耗尽自动重试，可修正凭据或连接问题后，对它选择「立即重连」。CLI 的 `recovery-requested` 表示已受理恢复，随后在运行状态核对实际建连。恢复读取该 App 的原状态和回执，重新获得三次自动重试。退出期间选择等待或取消会返回 `worker-offline`，原等待意图留待恢复后的接收任务处理。

「最近观察到的变化」可查看同一 App 的连接变化，以及待核对和待送达计数的增加或归零。时间为本机观察时刻，约 5 秒的采样间隔可能漏过短暂变化。运行页展示最近 6 条，接收进程重启后从新样本开始记录。受管实例的 `service logs` 可在系统日志保留范围内查阅 `receiver-diagnostic` 条目，其中包含 App、观察时间、事件类型和计数。

`service logs` 的 `receiver-metric` 条目按 App 记录指标。`ws_reconnect` 每次飞书 SDK 重连成功记一条。`run_e2e_ms` 从写入本轮提交记录前的本机时间，计到 Bridge 核实 T3 运行收口并保存完成或失败终态。耗时以毫秒计，包含 T3 排队、审批、等待异步子任务和核对的时间。旧运行缺少起点时无耗时记录。

用 `t3-bridge service logs -instance work -lines 200 -telemetry` 读取本机受管实例的 JSONL。每条记录包含 `type`、`tenant`、`appId` 和 UTC 时间 `at`。`receiver-metric` 包含 `name`、`value`，运行耗时还带 `terminal`。`receiver-diagnostic` 包含 `kind` 和 `count`。`receiver-failure` 的 `kind` 区分状态打开、账号绑定、首次连接、入站、长连接退出和工作循环故障。`receiver-event` 的 `kind` 标记运行受理、收口、最终回复送达或飞书重连。采集范围受平台日志保留策略、每个输出流的请求行数及 128 KiB 读取上限限制。Web 和桌面可在服务日志页选择「查看遥测」，TUI 可在受管实例页按 `t`。

共享实例可在遥测页按租户和 App 筛选。Web 和桌面的下拉框取自最近读取的日志，刷新时保留筛选。TUI 用 Tab 或 Shift+Tab 在同一次读取中切换 App。CLI 可在 `-telemetry` 和 `-telemetry -follow` 后附 `-tenant feishu -app-id cli_example`，同时精确匹配租户和 App ID。读取窗口先按实例日志和行数确定，筛选后为空表示该窗口内没有匹配记录。

接收端绑定 App 后，将同一字段白名单内的遥测追加到该 App 状态目录下的 `telemetry/bridge-YYYY-MM-DD.jsonl`。文件按 UTC 日期划分。启动和跨日后首次输出遥测时清理该目录内到期的私有归档文件。默认保留含当天在内的 30 个 UTC 日。在接收进程环境中设置 `LARK_CHANNEL_LOG_DAYS` 可指定 1 至 3650 天。无效的值使用默认天数。宿主管理的 stdout、stderr 和 journal 由各平台自行保留。

```sh
t3-bridge config telemetry-archive -profile work -app-id cli_example -date 2026-11-20 -lines 100
```

归档命令只读已保存配置和所选 App 的日文件，接收端停机后也可使用。省略 App ID 时只读主账号，省略日期时读当天 UTC 日。命令从文件末尾最多读取 128 KiB，输出其中最后 1 至 1000 条匹配且完整的 JSONL 记录，默认最多 100 条。当日归档不存在或没有匹配记录时输出为空。

Web 和桌面可从已保存的配置打开「遥测归档」，按 App 和 UTC 日期浏览。前后翻日时保留所选配置和 App。在 TUI 的账号总览按 `Y`，输入 UTC 日期后按 Enter。结果页用 `[` 和 `]` 翻日，`r` 刷新，`e` 输入其他日期，Esc 返回原账号和草稿。三个工作台与 CLI 使用同一个归档读取器，按日最多显示 100 条匹配记录。

用 `t3-bridge service logs -instance work -telemetry -follow -lines 0` 持续接收新增 JSONL，可将标准输出接入本机采集器。省略 `-lines` 会先读取最近 100 条原始日志。Linux 按 systemd 用户单元读取 journal。macOS 和 Windows 约每半秒读取一次 stdout 和 stderr，每次每个文件最多 128 KiB。持续高吞吐可能造成读取滞后，快速轮转或截断可能跳过尚未读取的内容。流式读取使用平台现有日志和同一字段校验，按 `Ctrl+C` 退出。

命令故障使用 `command_fail`，`value` 固定为 `1`，`step` 标明故障阶段。采集包括处理与持久化、原回复与降级回复、送达窗口到期、重连、模型回声、工作区、群读取与建群、会议，以及宿主关闭请求。回复、建群和欢迎消息用原回执识别各阶段的首次尝试，进程中断时样本可能缺失。Bridge 的 ACL 拒绝、用法提示和正常等待属于预期结果。输出包含记录类型、租户、App ID、时间、指标名称、数值和阶段。

运行页同时显示并发额度的占用、上限和等待数。待核对请求按派发规则保留额度，等待数仅统计因额度阻塞的任务。`pool_active` 和 `pool_waiting` 在首个诊断样本及计数变化时写入日志。

Windows 在当前交互用户的普通权限终端运行以上管理命令。新注册任务的日志位于 `%LOCALAPPDATA%\t3-bridge-service-logs\<实例名>\`。接收进程非零退出后，Bridge 每隔 1 分钟重试，单次服务启动最多重试 3 次。正常退出后停止重试。等待重试时计划任务仍可显示运行中，可用 `t3-bridge ps` 和运行页核对实际接收进程，或在服务日志查看退出和重试次数。

`service stop` 先禁用任务，再结束 Bridge 监督进程及其 Windows Job 中的接收进程，取消本地后续重试和登录自启。旧任务仍可启停。要切换到 Bridge 自带的重试，排空后依次 `stop`、`unregister` 并用原实例名和配置重新 `install`，再按需要 `start`。注销后保留配置、状态和日志。切换前先排空，停机后核对 T3 原 run 和 `config drain`。 [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects) · [Task Scheduler 停止接口](https://learn.microsoft.com/en-us/windows/win32/taskschd/registeredtask-stop)

在 Windows 用专用实例做恢复验收。由 `config init -template generic -config <临时路径>` 生成禁止接收的配置，可复现启动时的非零退出。观察新增日志并记录各次出现时间，核对重试间隔和次数。在重试等待期执行 `service stop`，确认任务已禁用，并等待超过 1 分钟核对是否还有启动。完成后注销测试任务。

### 按配置核对接收就绪

启动或重启后，从 Web 或桌面工作台的已保存配置打开「接收就绪状态」。TUI 在账号总览按 `Ctrl+P` 核对整份已保存配置，接收诊断仍可按 `Ctrl+D` 查看当前 App。CLI 可直接按配置查询，无需先从 `ps` 抄录进程 ID。

```sh
t3-bridge config receiver-readiness -config "$CONFIG"
```

结果按租户和 App 列出未启用、未启动、等待样本、连接中、重试中及其他状态。当所有已启用 App 均由唯一进程加载此配置的实际 SHA-256 版本，最近样本核对 T3 环境通过、飞书 WebSocket 已就绪，且 App 未等待重连时，CLI 返回 `ready: true` 和零退出码。没有已启用 App、样本过期、现有进程仍加载旧内容，或旧版进程未记录版本时，结果仍可读，但就绪检查以非零退出。

查询读取本机诊断快照，不切换配置、连接或服务。可用 `-expected-revision` 固定已保存的 SHA-256，或用 `-profile` 选命名配置。版本反映进程启动时加载的 JSON 内容。连接样本和真实飞书首条消息分别验收。

在启动或重启脚本中可加 `-wait 30s`。命令以开始时读取的保存版本为准，等到就绪才成功退出。超时返回最后一次快照和非零退出码。等待期间配置版本变化时直接报错。命令正常返回或超时时，各输出一份 JSON。

### 接入旧桥遥测模块

发布包附带可选的 Node 兼容转接器。在发布包目录使用 Node 20.12 或更新版本，把已有受管实例的遥测流接到模块。脚本接受本地模块文件，也可使用发布包目录可解析的已安装包名。`--module` 省略时使用旧环境变量 `LARK_CHANNEL_TELEMETRY_MODULE`。 [Node 模块解析](https://nodejs.org/api/esm.html#resolution-and-loading-algorithm)

```sh
t3-bridge service logs -instance work -telemetry -follow -lines 0 |
  node deploy/lark-telemetry-relay.mjs --bridge-version "$(t3-bridge --version)" \
    --module "/absolute/path/to/bridge-monitor.mjs"
```

转接器支持默认导出和 `createAdapter`，按租户及 App 分别创建实例。工厂收到 Bridge 版本、App ID、租户和主机名。诊断及故障调用 `emit`，故障还会调用 `recordError`，指标调用 `recordMetric`。运行完成与失败映射为旧标签 `done` 和 `error`。新版的 `ws_reconnect` 表示重连成功，命令故障沿用新版的阶段标签。

运行事件使用旧事件名 `run.started`、`run.completed`、`run.failed`、`outbound.sent` 和 `ws.reconnected` 调用 `emit`，`fields` 与 `ctx` 为空。`run.started` 表示 Bridge 已确认并保存 T3 Run ID，该轮也可能仍在 T3 排队。运行终态以原 Run 核对并保存为准。`outbound.sent` 表示飞书确认终态 IM 回复已创建或更新，并且 Bridge 已保存回执。进程若在回执落盘与日志输出之间退出，事件可能缺失。

模块在独立进程运行，由该模块决定上报目的地。其他旧日志事件、原日志字段、逐轮用量和费用没有对应输入。结束读取时会尝试 `flush` 和 `close`，所有 App 共用两秒异步清理预算。收到停止信号时，正在执行的异步回调也有两秒等待上限。超时返回非零退出码。同步回调阻塞 Node 时，可由宿主终止转接器进程。

### 接收进程

`t3-bridge ps` 列出当前用户的前台和托管接收进程。用 `t3-bridge kill PROCESS_ID` 请求指定实例正常退出。完整实例 ID 每次启动生成，PID 供排障查看。对于托管服务，用 `service stop` 关闭自启。接收进程退出后，另行核对在途 T3 工作。

用 `t3-bridge --version` 查看手头的二进制版本。`ps` 显示进程登记时的接收端版本。Web 和桌面工作台的「运行状态」也同时列出工作台和接收进程版本，可在升级后核对实际运行的程序。

同一进程接收多个 App 时，接收任务失败只影响对应 App。该 App 的就绪快照会清除，进程每隔 30 秒为它重试一次，每轮最多重试三次。预算耗尽后可在进程日志核对该 App 的停止记录。共享进程仍有其他接收任务运行时，「立即重连」可恢复已退出的单个 App。若所有 App 均因故障退出，进程返回失败，由宿主服务按自身策略恢复。单 App 实例直接交由宿主服务处理。

### 停机后核对全部 App

暂停新输入，先核对 T3 在途轮次、审批、子任务和原飞书回复。让原接收端保持连接，等待它按原 Run ID 核对停止后的收口。只有原轮次和委派工作已收口时，接收端才会保存核对结果。停机后用同一份已保存的私有 JSON 检查全部 App。

- CLI 使用 `t3-bridge config drain -config <配置路径>` 或 `t3-bridge config drain -profile NAME`。JSON 包含总结果和逐 App 状态。只有所有账号都已排空时退出码才为 0。
- TUI 从配置浏览页按 `G`。Web 和桌面工作台从已保存配置打开排空检查。各入口都显示入站、在途、待核对和未结回执计数，并按 App 列出最多八条可定位的原消息。Web 和桌面可以直接查询该条回执，TUI 可返回对应 App 按 `v` 查询。待核对总数也包含缺少原消息 ID 的记录。

检查包含当前已禁用的账号。各类计数可能重叠。缺少状态、锁被占用、状态损坏或归属不符时，结果为未确认。缺少原轮次收口证据或回复仍待送达时，结果为阻塞。继续切换前，按原账号核对状态及实际收尾。检查只读取本机持久记录，状态锁在检查结束后释放。请在整个切换窗口内保持原接收端停机。已有逐 App 脚本可继续使用 `-drain-check -app-id APP_ID`。

在线「送达缺口」、停机后的排空检查和旧逐 App 命令都计入尚未确认的终态回复与过程卡。只保存了上一帧进度的终态卡仍待送达。旧记录若只有与终态正文一致的通用通知凭据，排空沿用旧规则，单条回执仍将该轮终态投递显示为未知。

### macOS 从 lark-channel-bridge 切换

1. 从旧 profile 导入候选，使用私有凭据文件和独立状态目录。配置实际的 T3 目标和逐路由用户，运行 `config check`。保持候选接收关闭时，可用 `config preflight`、TUI 安装页的 `p`，或 Web 和桌面安装页的「检查启动条件」查看逐 App 的本机接收占用。用 `service render` 核对新 LaunchAgent 的程序和配置路径。保留旧桥的服务定义、配置和状态。 [候选导入及凭据复用](CONFIGURATION.zh-CN.md#从-lark-channel-bridge-导入配置)
2. 使用同一 App 的切换窗口先暂停输入。通过旧桥的 `/status` 和 T3 核对在途工作、审批及子任务，确认原回复已送达。用旧桥的 `stop --profile NAME` 停止接收并关闭登录自启，再用 `status --profile NAME` 核对。
3. 同 App 的旧 T3 私聊可在启动前预览并登记原线程，随后按原入站 ID 核对第一轮续聊。启用候选的 `receiverAuthorized` 后安装并启动新实例。在运行页核对新进程、App、飞书连接和 T3 环境。 [私聊会话转接](CONFIGURATION.zh-CN.md#转接旧桥的私聊-t3-会话)
4. 回退时先核对并排空新端在途工作。用 `t3-bridge service stop -instance NAME` 停止新接收端并用 `config drain` 核对全部 App，再用旧桥的 `start --profile NAME` 恢复接收。保留两端的状态与回执供后续核对。

需要在回退后续聊时，在停机窗口核对旧活动目录和新版回执是否指向同一 T3 线程，以及旧桥能否继续使用该线程的执行配置。旧桥会从自己的活动目录选择线程。再次启用新版可沿用原配置与状态目录。

新版接收端处理已登记转接范围的首批输入时，会核对有界的旧 T3 历史与飞书原消息。确认属于同一机器人、聊天、话题和发起人的旧消息 ID 会记入持久去重记录。已入队的重投会在合并之前移出，转接记录留给下一条新消息。超过读取上限或缺少原消息证明的输入继续走既有核验。回退旧桥时，旧桥仍使用自己的消息去重状态。

从旧桥再次启用新版并沿用原 Store 时，首批输入也会核对已经转接且当前轮次已结清的原线程。新版会保留既有 Go 回执，把可核实的回退期间旧消息加入去重记录。下一条新输入仍可接续原线程。

如果新版期间用 `/new` 开了独立线程，回退前用 `config receipt` 查新版最近一轮的 `threadId`，并用 `config import-session -list -chat-id "$CHAT_ID"` 查看旧活动目录。两个 ID 不同时，回退后的旧桥沿用旧目录线程，再次启用新版时沿用原 Store 里的独立线程。保持同一 App、聊天范围和路由目标时，新版会从唯一可核实的旧转接线程拦截重投，后续新消息继续发往新版独立线程。同一范围有多个可能的旧线程时，保留正常输入处理。

本机检查会核对当前用户配置目录中同租户同 App 的 t3-bridge 锁、`LARK_CHANNEL_HOME` 或默认旧桥目录中的 App 锁心跳，以及已选状态目录的锁。状态目录锁也适用于沿用同一目录升级早期 t3-bridge 实例。任何一项占用都会让已启用账号的预检失败。旧锁过期或无法核对时显示「未确认」，请核对原实例。接收关闭的账号只显示本机检查，跳过飞书与 T3 查询。结果是本机即时记录，跨机器和正式切换仍按停旧再启新的顺序核对。

启动预检也会列出仍使用内置模板占位值的配置字段。草稿可照常校验和保存。启用账号存在占位字段时，先补齐配置，再执行飞书与 T3 预检。实际启动同样会在创建接收状态前拒绝这类配置。显式标记为不可用的路由会在预检中显示为已停用。

旧锁仍有心跳且记录可核对时，预检会显示记录中的旧 profile 和 PID。先用旧桥的 `status --profile NAME` 或 `ps` 核对实例，再按切换流程停止对应 profile。如果旧锁记录缺失，按预检显示的 App ID 在旧桥中确认占用来源。

### macOS 已有 t3-bridge 实例升级

1. 用候选二进制运行 `config check -config <配置路径>`，核对现有 JSON。确认 LaunchAgent 实际加载的定义和程序路径。旧自定义 LaunchAgent 沿用其原管理方式，`service` 管理由该命令安装的实例。
2. 在切换窗口暂停输入。逐 App 核对 T3 原 run、审批、子任务及送达后停止接收。用候选二进制执行 `config drain`，核对全部 App 后备份程序、服务定义及私有配置和状态。升级沿用同一 App 的状态目录和回执。
3. 启动候选后，用工作台的「接收就绪状态」或 `config receiver-readiness -config "$CONFIG" -wait 30s` 核对新配置版本及各 App 的飞书和 T3 就绪情况。在授权测试会话中走通新消息、续聊和停止，核对原线程及结果回执。确认或回退前保留上一版可执行文件。
4. 回退前再次停止并排空。用上一版确认当前配置和状态可读，并核对新增状态字段与旧版的兼容性。若旧版无法保留切换后新增的任务或回执，先按原线程和消息记录对账，再继续回退。

## 云文档评论

1. 使用具备 Drive 评论读取与回复权限的独立飞书应用，订阅长连接事件 `drive.notice.comment_add_v1`。扫码时可以预选这些权限。已有应用在开放平台核对权限、事件订阅和发布状态。
2. 让测试文档授权给该应用。在账号配置中填写 `commentRoute`，将测试人加入账号和该路由的用户范围。
3. 在文档行内评论和同一评论的后续回复里 @ 机器人。核对运行时原回复出现「敲键盘」，结束后表情消失，两次答复各自回到原评论线程，并在 T3 续聊。全文评论的结果以新的全文评论交付。支持 `doc`、`docx`、`sheet` 和 `file`。知识库节点可在具备权限时解析到这些文件类型。
4. 用另一位未经路由授权的评论者验证同样的 @ 不会触发 T3。两位用户有各自的 T3 会话。选区和此前的评论文本按限额传入，新版文档在账号有权限时附带开头最多 6000 字的正文。结果使用飞书评论的纯文本格式。
5. 从管理员私聊查看评论任务。对两个评论线程分别读取空闲超时，修改其中一个后核对另一个的值。停止一条测试轮次，确认 T3 仍指向原 run，终态回复仍在原评论，再发一条新评论核对新会话。

在飞书聊天发送 `/doc` 可查看当前账号是否开放评论入口。配置的评论路由和文档访问权限均需满足。运行开始后，Bridge 尝试在已定位的原评论回复上添加「敲键盘」表情。停止或收口时按原回复 ID 尝试清除。应用缺少表情操作权限时仍会处理评论。

应用所有者或管理员可以在已授权私聊管理这个 App 的评论任务。

卡片模式下，可从 `/status` 进入「评论任务」，也可发送 `/doc tasks`。列表可翻页和刷新。在对应状态行下点「停止」并确认。评论线程的空闲超时命令也显示在卡片底部。

- `/doc tasks [页码]` 查看任务阶段和轮次令牌。列表仅显示运行元数据。
- `/stop comment:<令牌>` 请求停止所选轮次。T3 按原回执执行停止。若停的是当前会话，后续评论会从新的 T3 会话开始。
- `/timeout comment:<令牌>` 查看所选评论线程的空闲超时。加上 `1..120` 分钟、`off` 或 `default` 可修改。覆盖应用于在途轮次及该用户在同一评论线程的后续输入。同一文档的其他评论线程可有各自的设置。

轮次变化后重新用 `/doc tasks` 获取令牌。评论回复各自保留原入站回执和投递位置。

最终答复在运行收口后发布。发布请求结果未知时，Bridge 保留记录供人工核对，避免重复评论。

## 独立实例验收

使用独立飞书应用、至少两个测试账号、一个测试群、专用的 T3 测试项目和空状态目录。记录应用、环境、项目、provider、模型和权限的读回结果。验收记录只保留必要的脱敏标识和回执。

1. 在私聊发送带唯一标识的普通消息，核对飞书结果卡片及 T3 的线程和 run。再发一条追问，确认在同一线程产生下一轮。用 `/result` 核对两轮结果。
2. 在测试群验证授权和 @ 规则。分别用允许和未授权的账号发消息，检查未授权消息无对应 T3 run。用已授权账号测试无权路由，检查拒绝结果及实际目标。
3. 对测试任务发 `/stop`，在飞书和 T3 核对收到的停止状态。确认任务结清后，再验证后续普通消息可独立处理。
4. 在另一个测试 run 在途时重启 Bridge。核对恢复后的线程、run 和原卡片，确认同一入站消息没有产生第二次提交或重复结果。
5. 演练停止测试接收端和回退，核对保存的状态、消息去重及停止回执。接收端停机后用 `config drain` 核对全部 App。有未结运行时，先在 T3 核对原 run。
6. 验收旧私聊转接时，先由旧桥用同一专用 App 生成一条测试会话并正常停机。按测试私聊 ID 找到原线程，核对项目并在离线目标登记。启动新接收端后，用授权用户在原私聊发一条新消息，核对回执仍指向同一线程且产生新的 run。再从其他测试范围输入，核对其回执没有关联该线程。 [私聊转接操作](CONFIGURATION.zh-CN.md#转接旧桥的私聊-t3-会话)
7. 验收双身份时，为一个测试用户配置本机专用 provider。通过 `config check-tool-identity` 后，在该用户的私聊分别读取应用与个人可见的测试资源，并在执行端核对身份。用另一用户和测试群验证派发拒绝，再撤销测试用户授权，核对从原私聊续接时也会拒绝派发。 [双身份配置](CONFIGURATION.zh-CN.md#专用应用与用户双身份)

### 按飞书入站消息核对回执

在验收或排障时用原入站消息 ID 查询本机持久回执。可以选择命名配置中的附加 App，省略 `-app-id` 时查询主账号。

```sh
t3-bridge config receipt -profile work -app-id "$APP_ID" -message-id "$FEISHU_MESSAGE_ID"
```

TUI 选中账号后按 `v` 查询。Web 和桌面从工作台打开「消息回执」，选择已保存的配置及 App。

数据来自所选 App 最近一次落盘的状态快照。能读到当前轮、同一会话的历史轮和仍在入站队列中的消息。合并成员会返回代表此次提交的 `inputId`。回执列出原环境、项目、thread、run、请求和回复 ID。缺少 run 或回复 ID 时，按当前阶段核对。

「终态回复投递」对应 CLI 的 `deliveryState`。有进度卡时也可能显示等待终态回复（`pending`）。`confirmed` 表示 Bridge 已保存该轮终态回复的飞书 API 确认。`unconfirmed` 表示已有投递尝试需要核对。旧状态缺少独立的终态确认时显示 `unknown`。另行投递的会中消息或会后纪要使用自己的回执，此处显示 `not_applicable`。飞书 API 确认可用来核对回复发送或更新，读取状态以飞书客户端为准。

普通卡片创建回执丢失后，Bridge 用原 UUID 找回卡片，再更新同一张卡。终态更新未确认时，同一会话的续聊会等待，避免后续轮次覆盖待送达的结果。需要独立开始时可用 `/new`。

如果 T3 返回了与原环境和线程一致的 HTTPS 会话地址，CLI 回执包含 `threadUrl`，TUI 显示完整地址，Web 和桌面可在浏览器打开该会话。打开后按 T3 的访问授权进入。旧回执仍可用线程和 run ID 核对。

升级或重启前后可对同一消息执行查询，核对本机记录的 `requestId`、`threadId`、`runId` 与回复 ID。实际去重和送达还需到原 T3 与飞书会话核对。

## 会话命令

完整语法、别名和权限见 [飞书斜杠命令手册](FEISHU-COMMANDS.zh-CN.md)。

| 命令 | 行为 |
| --- | --- |
| `/help`、`/status` | 查看命令、当前目标、会话和在途任务 |
| `/status tasks [聊天ID[:话题ID]] [页码]` | 所有者或管理员从已授权私聊查找当前 App 的私聊、群聊及话题任务 |
| `/doc`、`/doc tasks [页码]` | 查看评论入口。所有者或管理员可从已授权私聊查看任务令牌并管理指定评论轮次 |
| `/ps` | 查看当前应用的本机接收实例 |
| `/account`、`/account change` | 所有者或管理员在私聊查看接收 App、租户和连接状态，或获取本机换绑步骤 |
| `/exit <实例ID>` | 单应用实例的所有者或管理员可在已授权私聊请求关闭。多应用共用实例由宿主 CLI 管理 |
| `/new`、`/reset` | 清除当前映射，运行中的 Bridge 任务进入停止流程 |
| `/new chat [群名]` | 在当前受控项目上下文建私有群 |
| `/resume`、`/resume page <页码>` | 分页查看本人当前范围的可恢复会话，每页八条 |
| `/resume <短令牌>`、`/resume use <短令牌>` | 切换到本人同范围的会话 |
| `/resume --stop <短令牌>` | 请求停止本人当前轮次后切换到所选会话 |
| `/stop`、`/cancel` | 停止本人当前任务或明确引用的 Bridge 任务 |
| `/stop task:<令牌>` | 所有者或管理员在已授权私聊请求停止所选聊天轮次 |
| `/cd [绝对路径]`、`/ws` | 查看、切换或保存当前受控项目。运行中可用 `/cd --stop <绝对路径>` 或 `/ws use --stop <名称>` 请求停止本轮并切换 |
| `/cd list [页码]`、`/cd use <项目ID>` | 所有者或管理员在已授权私聊浏览当前路由所在 T3 环境的已登记项目，确认后切换 |
| `/review <问题>` | 使用单独配置的复核路由 |
| `/result [轮次令牌] [页码]` | 读取脱敏的当前或历史轮次结果 |
| `/doctor`、`/doctor --echo [routeId]` | 查看接收端版本和已知连接状态。所有者或管理员可在已授权私聊点选诊断路由，也可在已授权群聊或话题输入命令，回声结果由同一应用私信发起人 |
| `/reconnect` | 管理员重连 Bridge 客户端。附加 `--wait` 等待在途工作，`--cancel` 取消等待 |
| `/timeout` | 查看当前轮次及本范围的超时设置。卡片模式可编辑两种超时 |
| `/timeout deadline <秒数\|off>` | 为当前会话范围设置 1 至 86400 秒的绝对期限。现有 `/timeout <秒数\|off>` 同样可用 |
| `/timeout idle <分钟\|off\|default>` | 为当前轮次及同一范围的后续轮次设置 1 至 120 分钟的空闲超时、关闭或恢复默认值 |
| `/config`、`/config run` | 所有者或管理员在已授权私聊编辑本 App 的运行设置 |
| `/config route [页码]`、`/config route use <路由ID>` | 在已授权私聊按项目、模型和运行模式查看可用路由，确认后设为自己的默认路由 |
| `/config access` | 查看群授权和用户、管理员、路由授权命令。群内的 `/config` 也显示当前群设置 |
| `/config access user [页码]`、`/config access admin [页码]`、`/config access route <路由ID> [页码]` | 所有者或管理员在已授权私聊查看当前 App 的配置值、运行覆盖和本层生效权限 |
| `/config groups [页码\|群ID]` | 所有者或管理员在已授权私聊查看本应用所在群，并在群详情确认修改授权或 @ 设置 |
| `/config mention check`、`/config mention authorize` | 所有者或管理员在已授权私聊检查飞书的全部群消息权限，按需生成当前 App 的补授权链接 |
| `/config display` | 所有者或管理员在已授权私聊点选回复模式和过程设置，每张卡改一项 |
| `/config identity`、`/config identity use <路由ID>` | 所有者或管理员在已授权私聊点选同项目的专用工具身份路由，也可用文字命令选择。所选路由成为该私聊的默认路由 |
| `/config reply [card\|text\|markdown]` | 查看回复模式，由管理员为之后的新回复选择卡片、终态纯文本或终态 Markdown |
| `/config tools [off\|brief\|detailed\|default]` | 调整当前账号的公开工具活动显示。简略显示一条，详细显示最多五条的类型、状态和时间 |
| `/config process [off\|brief\|detailed\|stream\|stream-brief\|stream-detailed\|card]` | 由管理员为后续聊天轮次设置过程展示，默认关闭 |
| `/config idle-timeout <分钟\|off>` | 由管理员为后续轮次设置默认空闲超时。初始关闭 |

应用所有者或管理员可通过 `/invite group`、`/remove group` 管理群授权。`/config mention all [on|off]` 查询或修改当前应用的群聊默认 @ 要求，初始为 `on`。在群内使用 `/config mention on|off` 覆盖本群规则，使用 `/config mention default` 恢复继承。`/invite` 和 `/remove` 可更改用户、管理员和路由的动态权限。`/config concurrency` 和 `/config progress` 更改运行预算和卡片进度。实际执行仍按目标 ACL 和 T3 身份核对。合法撤权会冻结相应待派发输入。

#### 在私聊选择默认路由

直接发送 `/config route`，按项目、模型和运行模式选择一条已授权路由。卡片每页显示六条。也可发 `/config route 2` 查看第二页，或发 `/config route use <完整路由ID>` 直接选择。等当前私聊中的运行和排队消息结清后再切换。

切换只保存当前用户在这个私聊的默认路由。发下一条对话时，Bridge 才核验并使用该目标。切回原路由时可恢复其原有会话。显式路由前缀、引用回复和已配置的分类器继续按现有规则分派。

#### 回复与过程显示

纯文本轮次结束后发送结果，可引用原回复续聊或用 `/result` 读取全文。运行期间可通过 `/status` 查看状态，通过 `/stop` 停止。

引用消息、展开合并转发或补读同一话题的前文时，Bridge 会向飞书读取原始卡片。卡片 1.0、2.0 和飞书转换后的行式正文均可提取。标题、正文和支持的分栏文字会作为引用资料进入 T3。读取仍按原聊天与话题核对可见范围。 [飞书卡片结构](https://open.feishu.cn/document/feishu-cards/card-json-v2-breaking-changes-release-notes?lang=en-US)

一次引用或合并转发遇到飞书短暂超时或网关故障时，所有查询共用一次自动重试。读取完整且通过范围校验后才会派发。遇到限流、权限拒绝或仍无法读取时，原消息会收到对应提示。可以稍后重新引用或转发，也可以直接发送需要处理的文字。 [飞书通用错误码](https://open.feishu.cn/document/server-docs/api-call-guide/generic-error-code)

独立过程消息使用原聊天或话题中的单张卡片。`brief` 和 `detailed` 显示运行阶段及工具活动。`stream` 随 T3 发布的公开正文快照更新，包括仍在运行且来源已核验的助手片段。`stream-brief` 和 `stream-detailed` 在同一张过程卡上同时显示正文和工具状态。简略显示最近一项工具状态，详细显示最多五项及其时间。过程卡按原回执更新，终态移除正文预览，最终答复沿原结果回复送达。

`card` 把公开正文预览放在原结果卡上。它适用于卡片回复，并随 `/config progress` 开关即时显示或隐藏。终态结果更新同一张卡。终态更新待确认时，原会话的续聊会排队等待，也可用 `/new` 开启新会话。两种正文预览均最多显示 1,600 字符。正文更新节奏取决于 T3 项目和模型的输出设置，T3 订正正文时预览随下一次核验更新。 [T3 流式写入](https://github.com/pingdotgg/t3code/blob/7d32075e3757ded707914e2450c3e392a9665765/apps/server/src/orchestration-v2/assistantStreaming.ts#L154-L241)

正文预览也可显示本轮已完成的「T3 计划草案」。终态交付保留助手答复，并附上已核实的收口轮次中最新完成的草案。只有草案时照常送达。长草案通过 `/result` 分页查看，读取结果不完整时会提示到 T3 会话核对。需要批准或回答问题时仍在 T3 中处理。 [T3 消息投影](https://github.com/pingdotgg/t3code/blob/v0.1.10/apps/server/src/mcp/OrchestratorMcpService.ts#L1021-L1133)

终态 Markdown 支持标题、列表和代码块。回复该消息可以续聊，也可以发 `/result` 取完整结果。过程卡可单独开启。

终态纯文本和 Markdown 在关闭过程卡时，可在原消息显示「敲键盘」表情。运行结束后按回执清理。表情使用飞书应用的消息表情权限，最终结果按原回复流程送达。

卡片模式下，`/help` 可打开状态、工作区及会话历史，`/status` 可刷新或停止卡片所示的一轮。已有当前会话时，两张卡都提供「新会话」。确认后，下一条消息开始新上下文，历史记录会保留。原轮次仍在运行时，Bridge 同时记录停止意图并取消该轮排队的补充。切换工作区或进入新轮次后，请重新打开状态卡。卡片在 55 分钟内有效，点击按原操作者和聊天范围重新核对权限。

`/status` 显示所选会话最近一轮的阶段，包括等待并发额度、T3 审批或问题、子任务和终态回复。排队数统计本用户在同一聊天或话题中已关联该会话的输入。「待分流」统计该范围尚未关联会话的输入。已授权的管理员私聊也显示本应用的并发占用、上限和因额度等待的任务数，口径与工作台运行页一致。

父轮次先结束时，Bridge 会继续追踪 T3 委派工作。归属明确的合并通知和同名委派也可收口，结果仍回复到原飞书消息，回执保留原 Run ID。多个来源均符合通知或历史证据不足时会继续显示核对中，可从会话入口查看 T3 记录。

结果卡和状态卡有可核对的 HTTPS 会话地址时，显示「打开 T3 会话」。审批和问题在原 T3 会话处理。T3 只返回旧式线程引用时，卡片保留可复制的引用。 [T3 会话链接](https://github.com/pingdotgg/t3code/blob/v0.1.10/docs/orchestration-v2/orchestrator-mcp-server.md#t3_thread_read)

在状态卡点「超时设置」，或直接发送 `/timeout`，可分开填写绝对期限（秒）和空闲超时（分钟）。两栏填 `0` 各自关闭，空闲超时填 `default` 跟随应用默认值。修改绝对期限后，在途轮次从保存时重新计时。修改空闲超时会重新采样活动。只修改一栏时，另一栏继续原计时。迁移自旧桥的分钟数请用 `/timeout idle <分钟>`。现版无子命令的数字继续按秒处理，`/timeout default` 可恢复空闲超时继承。工作项目、请求轮次或保存值变化后需要重新打开表单。

管理员可从私聊状态卡直接打开「聊天任务」和已启用的「评论任务」。列表可刷新、翻页和返回状态。聊天任务卡沿用打开列表时的群聊或话题筛选。停止按钮绑定列表生成时的具体轮次，确认后回执发到管理员私聊。每张卡只确认一次停止，回执中的「返回任务列表」会读取新状态。

`/doctor` 打开报告时只读取已知状态。已授权的管理员私聊最多显示六个符合 `plan` 和 `approval-required` 的诊断路由。点击「模型回声」并确认后，Bridge 重新核对操作者和配置，再提交固定提示词。每张报告卡可发起一轮，结果回复到原卡。文本模式列出相同路由的命令。也可直接发送 `/doctor --echo <routeId>` 选择其他符合条件的路由。

所有者或管理员也可在启用了群访问的群聊及话题中发送 `/doctor --echo <routeId>`。群和路由须允许该次操作，个人工具身份路由仍限绑定用户的私聊。群内只确认已登记。测试通过或失败后，当前应用给发起人发送一条私信。Bridge 在派发和投递前复核原权限。遇到投递失败时，用原群消息 ID 在本机查询回执。

`/ws list [页码]` 每页显示八个别名。卡片可翻页、切换工作区或确认删除别名。在途时切换按钮会标为「停止本轮并切换」。删除只清除所选别名，项目、目录和会话都会保留。别名已改绑时，旧卡会要求重新打开列表。

所有者或管理员可在已授权私聊直接发送 `/cd list`。卡片按页显示当前路由的有效 T3 项目名称和 ID。可点选确认，或发送 `/cd use <完整项目ID>`。切换前会重新核对项目归属、路由和本私聊的在途任务。切换只更新当前工作项目，下条消息开启新会话。原历史可用 `/resume` 查看。当前项目可用 `/cd` 查看。

私聊 `/resume` 卡可翻页并选择本人当前项目中的会话。每张卡可确认一次切换。当前有另一个会话正在执行时，相应选择按钮会标为「停止本轮并切换到」。按钮绑定发卡时的原轮次。原轮次已换时请重新打开历史。只切换可用 `/resume <短令牌>`，明确停止并切换可用 `/resume --stop <短令牌>`。停止意图和新会话映射一起保存，远端停止结果在原结果卡查看。

`/cd` 使用目标机器的绝对路径。Windows 项目支持盘符路径（如 `C:\work\Project`）和共享路径（如 `\\files\Projects\Shared`），项目选择以 T3 读回为准。 [Windows 文件路径约定](https://learn.microsoft.com/en-us/windows/win32/fileio/naming-a-file)

停止并切换先核对新项目，然后一并保存本人的原轮次停止请求和新工作区。原工作区的排队续聊按停止规则取消，后续新消息进入新项目。旧轮次的远端停止结果仍在原回复查看。若工作区别名以选项开头，可用 `/ws use -- <名称>` 按字面切换。

`/reconnect --wait` 暂停当前账号的新派发，继续跟踪在途轮次和子工作。确认已回复等待回执、在途工作已收口后，Bridge 重建客户端并恢复派发。`/doctor` 显示等待状态和结果。管理员可用 `/reconnect --cancel` 取消任一入口的等待，或用 `/reconnect` 立即重连。

### 在飞书编辑运行设置

应用所有者或管理员在已授权私聊发送 `/config` 或 `/config run`，可在一张卡片编辑并发预算、默认空闲超时、进度、回复、工具活动、过程显示和群默认 @ 规则。点击「保存设置」提交。并发预算为 `0` 至 `50`，`0` 表示不额外限制。预算调整影响后续派发，默认空闲超时影响后续轮次。逐群 @ 覆盖继续优先于默认值。

保存时复核卡片所属账号、操作者和当前设置。设置已经从其他入口变化时，重新打开卡片再编辑。发送卡片被明确拒绝时，文本回复给出当前设置及对应命令。`/config display` 仍可快速切换回复和过程显示。

独立实例验收时，从一张卡修改并发和群默认 @，再分别从本机工作台和对应群核对生效值。通过本机入口更新一项后提交此前打开的卡片，核对旧提交被拒绝。

### 在私聊管理指定群

应用所有者或管理员在已授权私聊发送 `/config groups`。卡片列出当前应用已加入的群，每页六个。可翻页或按群名点开详情，也可用 `/config groups 2` 打开第二页，或附完整 `oc_` 群 ID 直接查看详情。

详情显示群授权和 @ 规则。选择授权、撤销或恢复默认后，用飞书弹窗确认。每张详情卡可提交一次。提交前会重新核对机器人是否仍在群中，以及卡片打开时的授权和 @ 设置。设置变化时，重新打开详情再操作。新设置只作用于该应用的所选群。关闭 @ 要求后，仍需核对飞书对无 @ 群消息的投递权限。

### 查看和批量修改用户权限

应用所有者或管理员可在已授权私聊发送 `/config access user` 或 `/config access admin` 查看名单。用 `/config access route <路由ID>` 查看指定路由。结果每页六条，按该 App 的 Open ID 排序，附上配置授权、运行覆盖和本层生效值。逐路由权限仍与聊天及用户权限共同生效。

`/invite user`、`/remove user`、`/invite admin` 和 `/remove admin` 后可以连续填写多个真实用户 @ 或本应用的 `ou_` Open ID。`/invite route` 和 `/remove route` 先填写精确路由 ID，再填写要修改的用户。重复身份只算一次。命令中的任一对象无效，或撤销涉及所有者保护时，整批权限变更均取消。撤销用户或路由权限时，相应的待派发任务一并拦截。

### 接收无 @ 的群消息

关闭 Bridge 的群聊 @ 要求后，飞书仍须授予应用 `im:message.group_msg`（获取群组中所有消息），并订阅消息接收事件。群授权和路由 ACL 继续决定是否处理已投递的消息。 [飞书应用权限说明](https://open.feishu.cn/document/develop-robots/add-bot-to-external-group)

在已授权管理员私聊发送 `/config mention check`，或用 `/config run` 表单关闭群默认 @，即可看到权限检查结果。明确缺少权限时可点「申请群消息权限」，也可发送 `/config mention authorize`。打开飞书授权页确认后，接收端会在链接有效期内定期查询。查到已授予时，原卡会显示最新权限。也可随时点「复查权限」。链接过期时重新申请。授权卡片不可用时，可在飞书开放平台为当前应用核对或申请权限。

在独立测试 App 上核对缺失、补授权和原卡自动更新，再从已授权群发送一条新的无 @ 消息验证投递。若仍未接收，从在线群授权查看该 App 的线上消息事件订阅。显示「未订阅」或「应用未发布」时，到飞书开发者后台检查 `im.message.receive_v1` 和应用发布。显示「已订阅」后，继续核对长连接订阅方式、应用可用范围和群范围。 [使用长连接接收事件](https://open.feishu.cn/document/ukTMukTMukTM/uYDNxYjL2QTM24iN0EjN/event-subscription-configure-)

### 在私聊停止指定聊天轮次

应用所有者或管理员在已授权私聊直接发送 `/status tasks` 查看当前 App 的聊天轮次。可加精确的飞书聊天 ID，或用 `oc_群ID:话题ID` 定位一个话题。列表显示轮次令牌、聊天类型、运行阶段和已记录的更新时间，每页 8 条。

复制要停止的 `task:…`，发送 `/stop task:<令牌>`。停止请求绑定原轮次，回执发回管理员私聊，任务状态仍由原聊天收尾。轮次已经变化时重新查询。原轮次停止后，该会话可按原有规则通过 `/new` 开新上下文。

已排队的轮次按 Run ID 取消。在途轮次按 Run ID 中断，Bridge 核对 T3 返回的线程与 Run ID。T3 仍有后台工作时，Bridge 会继续只读核对并保留并发占用。整线程停止和连带取消委派工作可在 T3 对应线程操作。 [T3 停止契约](https://github.com/pingdotgg/t3code/blob/v0.1.10/apps/server/src/mcp/OrchestratorMcpService.ts#L3413-L3497)

用专用测试群的两个话题各运行一个任务。从管理员私聊按话题找到一轮并请求停止，核对原回复和 T3 run，同时确认另一话题的任务继续运行。

### 接收账号换绑

所有者或管理员在私聊用 `/account` 确认接收 App。运行 `/account change` 可获取本机工作台步骤。同一 App 可扫码更新凭据引用或从「应用凭据」轮换密钥。更换 App ID 时，可扫码、输入新密钥或引用已有私有凭据文件。新草稿分配独立状态目录，只授予扫码用户或填写的初始用户访问权限，接收保持关闭。 [扫码替换](CONFIGURATION.zh-CN.md#扫码替换已有-app) · [输入新密钥](CONFIGURATION.zh-CN.md#输入新-app-secret-换绑) · [已有凭据文件](CONFIGURATION.zh-CN.md#使用已有凭据文件替换-app)

保存替换草稿前先备份旧配置、核对新 App 的应用 API 和目标，排空并停止原实例。保存后对新 App 预检，审阅启用，再启动实例。共用实例启停会影响该实例的所有 App。切换后在新账号私聊发送 `/account`，核对连接和接收身份。回退时先排空并停止新实例，再用旧配置和原状态目录恢复接收。

`/doctor --echo` 使用当前路由。指定路由 ID 可直接检查该路由配置的 T3 项目。目标需授权发起人，并配置 `approval-required` 和 `plan`。Bridge 新建独立线程，只提交固定提示和回执 ID。收到精确的 `OK` 后通过纯文本回复诊断结论。每个账号最多有一次在途回声，同一用户在 30 秒内只可发起一次。排队和运行共用一分钟时限。发送收据或停止结果不确定时，沿原回执核对 T3。

### 手动会议控制

按账号开启 `meetingEnabled` 后，应用所有者或管理员可从私聊运行以下命令。机器人入会后对参会者可见。飞书受理入会请求后，可在参会名单或等候室核对。

| 命令 | 作用 |
| --- | --- |
| `/meeting status` 或 `/meeting` | 显示当前操作者在此私聊发起的会议回执 |
| `/meeting join <9 位会议号>` | 提交一次入会请求。会议号可以含分组空格 |
| `/meeting leave [会议号]` | 用记录中的飞书会议 ID 离会。此私聊只有一场待关闭会议时可省略会议号 |
| `/meeting confirm-left <会议号>` | 从飞书确认机器人已离会或被移出后，手工关闭待核对回执 |

若结束事件先于入会响应，Bridge 会等同一 App 的原响应返回长会议 ID，再与结束事件核对。会议号和长 ID 都匹配后，直接显示会议已结束。响应丢失或证据不完整时，仍可核对参会名单和等候室后手工关闭回执。

#### 在本机管理会议

Web 和桌面工作台从「运行状态」选择对应 App 的「会议」。TUI 在账号总览按 `H`。页面列出该 App 的会议号、来源和回执阶段。提交入会、离会或关闭待核对回执前，确认完整 App、租户、进程和会议号。飞书受理入会后，在参会名单或等候室核对机器人是否已加入。

CLI 先使用 `t3-bridge ps` 找到同一 App 的完整进程 ID。会议号使用 9 位数字，`REVISION` 取自相同会议的当前回执。Lark 账号使用 `-tenant lark`。

```sh
t3-bridge config live-meetings -process PROCESS_ID -app-id APP_ID -tenant feishu
t3-bridge config live-meetings -process PROCESS_ID -app-id APP_ID -tenant feishu \
  -action join -number MEETING_NUMBER -confirm
t3-bridge config live-meetings -process PROCESS_ID -app-id APP_ID -tenant feishu \
  -action leave -number MEETING_NUMBER -revision REVISION -confirm
```

入会操作返回 `ended` 表示已核实会议结束并关闭入会回执。CLI 将此结果视为成功，TUI、Web 和桌面直接显示结束提示。

如果操作结果待核对，或飞书已结束会议但清单仍显示已受理，先在参会名单及等候室核对机器人已离会或被移除。Web 和桌面可复核后关闭对应回执。TUI 选中回执按 `c`，CLI 可用 `-action confirm-left -number MEETING_NUMBER -revision REVISION -confirm`。确认只关闭本地回执。入会开关控制新入会，仍可查看或关闭已有回执。通过本机发起的会议没有飞书私聊发起人。需要会后自动投递私聊纪要时，从已授权的飞书私聊发起。

#### 会前检查应用权限

在 Web 或桌面从已保存配置打开「会议权限预检」，选择 App 后提交检查。TUI 在账号概览按 `H`，进入会议页后按 `p`。接收端停机时也可检查已保存的 App。在 CLI 使用以下命令，省略 `-app-id` 时检查主账号。

```sh
t3-bridge config check-meeting -profile work -app-id APP_ID
```

检查显示该 App 的入会与离会、会议事件与字幕、会中发消息三项应用权限。检查还会核对飞书线上版本，逐项显示邀请、会中活动和会议结束事件的订阅状态。线上版本清楚但订阅数据不足时，订阅状态显示未知。CLI 的 `publicationState` 表示版本核对结果，`eventStates` 按事件名给出状态，`events` 列出三项待核对事件。明确缺少的权限会附上已定位到 App 和权限项的开放平台申请链接。CLI 从对应的 `authorizationUrl` 读取链接。按平台要求申请、发布并复查。 [飞书应用权限](https://open.feishu.cn/document/server-docs/application-scope/introduction) · [应用版本接口](https://github.com/larksuite/oapi-sdk-go/blob/v3.11.0/service/application/v6/resource.go#L405-L433) · [开放平台申请入口](https://open.feishu.cn/document/faq/trouble-shooting/how-to-fix-the-99991672-error)

如需检查活动会议接口，在 Web 或桌面选择「同时检查活动会议接口」，在 TUI 的权限结果页按 `v`，或给 CLI 命令加上 `-probe-availability`。探测用所选 App 的租户令牌查询应用所有者的活动会议，只显示接口状态。CLI 在 `availability` 中返回结果。主持人放行及事件实际送达仍需在独立会议中验证。 [官方 CLI 活动会议接口](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/vc/vc_meeting_list_active.go#L97-L121)

入会、离会的待确认回执跨重启保留，命令回放只重试原回复投递。离会失败会保留记录。收到应用和长会议 ID 均匹配的结束事件时自动结清回执。关闭配置后，原操作者仍可查询和结清已有回执。切换账号前需结清已受理或待核对的会议回执。请使用无需在聊天中发送密码的专用测试会议。

### 自动接受会议邀请

开启并配置 `meetingAutoJoinUsers` 后，名单中的管理员或所有者可直接在会议中邀请机器人。Bridge 核对应用、受邀机器人和邀请人，仅处理两分钟内的邀请，并通过原通话 ID 提交一次入会。请在参会名单或等候室核对。邀请人可在机器人私聊中使用 `/meeting status` 查看回执。首次对该会议执行字幕、问答或离会操作时，回执绑定当前私聊。 [飞书邀请入会协议](https://github.com/larksuite/channel-sdk-node#meetings--channeljoinmeeting--channelfollowmymeeting)

重复推送或接收端重启会沿用原尝试。飞书明确拒绝时，邀请人可用 `/meeting join` 手动尝试。结果待核对时，可用回执中的原会议 ID 请求离会，或在飞书确认机器人未留在参会名单或等候室后，用 `/meeting confirm-left` 结清回执。

### 字幕与会议问答

账号设置 `meetingRoute` 后，同一管理员可在发起入会的私聊使用以下命令。仅有一个活动回执时可省略会议号。多场会议请明确指定。

| 命令 | 作用 |
| --- | --- |
| `/meeting transcript [会议号]` | 查看已读取、去重的字幕摘录。分页未收全或摘录超限会标明 |
| `/meeting ask [会议号] <问题>` | 把用户问题和有界字幕交给该场会议的 T3 会话 |
| `/meeting notes [会议号]` | 在同一会议上下文整理决定、待办和未定事项 |
| `/meeting stop [会议号]` | 请求停止该场会议正在处理的原轮次 |

问答和纪要使用仍在跟踪的会议回执，结果发往原私聊。可引用原问题发送 `/stop`，或引用结果发送 `/result` 读取完整回复。Bridge 合并飞书长连接收到的字幕与按需分页补读，同一句保留修订内容。`/meeting status` 显示推送缓存句数。飞书分页暂不可用时，会返回仍可用的摘录并提示读取未完成。离会或收到匹配的结束事件后，Bridge 清除内存字幕。

### 会中公开问答

启用 `meetingRoomReplies` 后，已授权的参会者可以从会议聊天发送 `@bot /ask <问题>`、`@bot /notes` 或 `@bot /stop`。也可以将 `@bot` 换成机器人名称。飞书事件必须含有可核对的发言人、消息 ID 和近期时间。

每位发言人在同一场会议中有自己的 T3 会话。`/stop` 作用于本人当前轮次及之前排队的会中请求。结果待原运行和子任务收口后才公开发布。Bridge 为会中结果保存正文和 UUID。短时间的投递不确定会使用原 UUID 核对重试，明确拒绝、会议结束或超过重试窗口时保持已记录的投递状态。

### 会后自动纪要

在账号设置 `meetingSummaryTarget` 后，匹配的飞书会议结束事件会安排一份独立的 T3 纪要。Bridge 同时保存有界字幕和固定接收人，工作进程尝试补读飞书分页。纪要基于已取得的字幕，字幕可能不完整。没有可用字幕或接收人、路由权限无法核对时，执行记录会注明原因。升级前的会议回执缺少可核对的发起人时，跳过自动纪要。

运行和子任务收口后，Bridge 向指定私聊发送一条纪要。投给应用所有者时，每次发送前核对当前所有者。待确认的发送在 55 分钟内沿用原接收人、正文和消息 UUID。接收人变更、撤权或关闭配置时，Bridge 停止投递。已有 T3 运行继续按原身份核对。请在专用会议验证结束事件、字幕补读、接收人和客户端送达。 [配置规则](CONFIGURATION.zh-CN.md#会议机器人)

`/config tools` 与卡片进度开关独立。`default` 恢复原有展示方式。显式开启简略或详细时，运行中和终态回执均可显示当前主轮次的公开工具元数据。

空闲计时依赖同一 T3 run 的持续活动核对。检测到活动、审批、在途子任务或状态未知时，重新积累连续空闲时间。Bridge 重启后重新开始观察，设置仍保留。达到阈值时，Bridge 再核对原 run 后发出停止请求。

## 状态与故障

普通消息按用户、聊天或话题和受控目标续接当前会话。活动任务的入站消息持久排队。任务追踪保留 T3 thread、request、run 和飞书卡片的对应关系。父 run 结束后仍核对在途异步工作。

飞书入站事件缺少话题 ID 时，Bridge 对符合入站权限和 @ 条件的候选消息查群模式，必要时按原消息 ID 读回话题。已知话题群需要查到归属才能入队。配置了 `topics` 时，入队前还会核对话题白名单。群模式未知、话题未查到且未配置话题白名单时，沿用普通群路径。若机器人在新话题里没有响应，请核对应用能否读取该群信息及原消息。

状态载入或持久化出现错误时，保留状态目录并人工核对 T3 当前 run、发送和停止回执。测试更新时先用独立应用及状态目录走通升级和回退。监控自动调查、报告及旧轮次接管另见 [HomeOps 集成](HOMEOPS-INTEGRATION.zh-CN.md)。
