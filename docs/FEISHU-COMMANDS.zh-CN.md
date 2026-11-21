# 飞书斜杠命令

[English](FEISHU-COMMANDS.md) | 简体中文

在飞书或 Lark 中向 T3 Bridge 机器人发送命令。群聊或话题要求 @ 时，请用飞书的 @ 功能选中机器人。命令作用于收到消息的应用。在机器人会话中发送 `/help` 可快速查看命令。

下文方括号内为可选项，大写单词为待替换的值，页码从 1 开始。ID 和令牌请从对应命令的回复复制。**管理员私聊**指当前飞书应用的所有者或管理员与机器人的已授权一对一会话。使用管理员列表或选项命令时，请在私聊中直接发送新消息。涉及路由、聊天、话题和用户的操作仍按各自的权限核对。

- [对话、会话和结果](#对话会话和结果)
- [路由、项目和工作区](#路由项目和工作区)
- [超时和运行设置](#超时和运行设置)
- [群聊与访问权限](#群聊与访问权限)
- [接收端诊断与控制](#接收端诊断与控制)
- [云文档评论](#云文档评论)
- [会议](#会议)

## 对话、会话和结果

| 命令 | 作用 |
| --- | --- |
| `/help` | 查看机器人命令列表。部分条目取决于当前权限和应用配置。 |
| `/status` | 查看当前或引用轮次的路由、会话、运行、审批、子任务和投递状态。管理员私聊中还会显示接收端容量。 |
| `/new`、`/reset` | 归档当前会话映射，请求停止正在运行的轮次。下一条消息开启新的 T3 上下文，旧会话保留在历史中。 |
| `/new chat [NAME]` | 创建继承当前已授权路由和项目的飞书私有群。群名最长 60 字符，默认 `T3 Bridge · ROUTE_ID`。应用需要建群权限。 |
| `/resume`、`/resume page PAGE` | 查看本人在当前聊天或话题、路由和项目下可恢复的会话。每页最多 8 个。 |
| `/resume TOKEN`、`/resume use TOKEN` | 用 `/resume` 返回的会话短令牌切换会话。 |
| `/resume --stop TOKEN` | 请求停止当前轮次并切换会话。请在当前工作区直接发送。 |
| `/result` | 查看当前轮次脱敏后的最终结果。回复自己较早的结果可读取对应轮次。 |
| `/result TURN_TOKEN [PAGE]` | 用轮次短令牌或完整请求 ID 查看指定结果。翻页时使用 `/result` 返回的令牌。 |
| `/input` | 回复自己最新且可续聊的结果卡，在原卡打开补充文本框并提交。纯文本回复可直接发送补充消息或引用该回复。问题确认和审批在 T3 内处理。 |
| `/review [QUESTION]` | 通过已配置的复核路由发起独立只读复核。可引用自己的结果作为上下文。有可用轮次时可省略问题。复核独立返回结果，当前选中的会话保持原样。 |
| `/stop`、`/cancel` | 请求停止本人当前轮次。要停止本人其他在途轮次（例如复核），请回复其最新卡片或请求。过期引用会被拒绝。用 `/status` 查看结果。已完成的操作需要单独处理回退。 |
| `/status tasks [CHAT_ID[:TOPIC_ID]] [PAGE]` | 在管理员私聊中查看本应用的聊天轮次，每页最多 8 个。可用 `oc_` 开头的完整聊天 ID 或聊天 ID 加话题 ID 筛选，也可只传页码。 |
| `/stop task:TOKEN` | 在管理员私聊中，使用 `/status tasks` 返回的令牌请求停止指定轮次。管理员收到受理回执，原会话收到终态结果。 |

会话短令牌用于选择 T3 会话，结果令牌用于读取一轮输出，`task:` 令牌用于管理员停止指定轮次。例如，回复结果卡发送 `/result`，再把返回的轮次令牌填入 `/result TURN_TOKEN 2` 读取第 2 页。

## 路由、项目和工作区

路由决定消息在哪个环境运行，以及使用哪个项目、模型和交互设置。工作区命令在已授权的路由内生效。绝对路径指执行主机上的路径，支持 Windows 盘符或 UNC 路径。选择默认路由时，请在私聊中直接发送新消息。

| 命令 | 作用 |
| --- | --- |
| `/ROUTE_ID MESSAGE` | 将消息发给指定的已授权路由。斜杠后也可使用已配置的主机名或唯一的路由别名。独立复核路由使用 `/review`。 |
| `/config route [PAGE]` | 在已授权私聊中查看本人可用的项目和模型。每页最多 6 个路由。 |
| `/config route use ROUTE_ID` | 设置本人在该私聊中的默认路由。等当前及排队工作收口后再切换。显式指派和引用回复继续按正常路由规则处理。 |
| `/cd` | 查看当前项目。 |
| `/cd ABSOLUTE_PATH` | 切换到执行主机上已有的目录。路由和 T3 环境允许登记时，Bridge 可将目录登记为 T3 项目。 |
| `/cd --stop ABSOLUTE_PATH` | 请求停止当前轮次并切换项目。请直接发送，省去消息引用。 |
| `/cd list [PAGE]`、`/cd use PROJECT_ID` | 在管理员私聊中查看当前路由的 T3 环境内已登记的项目，并按项目 ID 选择。 |
| `/ws`、`/ws list [PAGE]` | 查看本人在当前范围内保存的命名工作区。每页最多 8 个别名。 |
| `/ws save NAME` | 为当前项目保存工作区别名。名称允许空格，最长 64 个可见字符。 |
| `/ws use NAME` | 选择已保存的工作区。用 `/ws use -- NAME` 可按原样读取以 `--stop` 或 `--` 开头的名称。 |
| `/ws use --stop NAME` | 请求停止当前轮次并切换到已保存的工作区。请直接发送，省去消息引用。 |
| `/ws remove NAME`、`/ws rm NAME` | 删除工作区别名。原项目、目录和会话仍可使用。 |
| `/config identity`、`/config identity use ROUTE_ID` | 在管理员私聊中查看或选择当前项目已配置的工具身份路由。路由需要兼容的本机 T3 绑定，并通过当前权限校验。 |

若已有路由名与 `account` 或 `doc` 重名，路由命令优先。为路由使用独立名称可保留这两个内置命令。

## 超时和运行设置

### 当前范围的超时

Bridge 按请求者、聊天或话题、路由和项目保存超时覆盖。可直接发送命令，或回复自己的最新轮次。更改可作用于在途运行。更新绝对期限会重新计时，更新空闲超时会重新观察活动。

| 命令 | 作用 |
| --- | --- |
| `/timeout`、`/timeout deadline`、`/timeout idle` | 查看当前超时设置。具备条件时打开设置卡。 |
| `/timeout deadline SECONDS`、`/timeout SECONDS` | 设置 1–86,400 秒的绝对期限。简写也按秒计算。 |
| `/timeout deadline off`、`/timeout off` | 关闭绝对期限。 |
| `/timeout idle MINUTES` | 设置 1–120 分钟的空闲超时。审批、活动、在途子任务或状态未知会打断连续空闲计时。 |
| `/timeout idle off` | 关闭当前范围的空闲超时。 |
| `/timeout idle default`、`/timeout default` | 继承应用的默认空闲超时。 |

### 应用设置

应用所有者或管理员可以修改运行设置。设置卡在管理员私聊中打开。

| 命令 | 作用 |
| --- | --- |
| `/config`、`/config run` | 在私聊打开运行设置表单。在群聊中单独发送 `/config` 会显示权限摘要。 |
| `/config display` | 打开回复与过程展示设置卡。 |
| `/config concurrency N` | 设置 0–50 的应用派发并发预算。0 表示关闭此 Bridge 限制。调低预算后，在途工作会继续运行。 |
| `/config progress on`、`/config progress off` | 开启或关闭可见进度更新。 |
| `/config reply [MODE]` | 查看或设置回复模式。`card` 更新结果卡，`text` 发送最终纯文本，`markdown` 发送格式化的最终回复。 |
| `/config tools [MODE]` | 查看或设置工具活动详情。可选 `off`、`brief`、`detailed` 或 `default`，其中 `default` 恢复继承。 |
| `/config process [MODE]` | 查看或设置过程展示。可选 `off`、`brief`、`detailed`、`stream`、`stream-brief`、`stream-detailed` 或 `card`。 |
| `/config idle-timeout [MINUTES]`、`/config idle-timeout off` | 查看或设置应用的默认空闲超时。取值 1–120 分钟，或用 `off` 关闭。单独设置的超时优先。 |

`brief` 和 `detailed` 在独立过程卡显示阶段及工具状态。`stream` 展示已核对的可公开正文。`stream-brief` 和 `stream-detailed` 在该卡上同时展示正文和工具状态。开启卡片进度时，`card` 将预览放在原结果卡上。 [回复与过程显示](RUNBOOK.zh-CN.md#回复与过程显示)

## 群聊与访问权限

以下命令需要应用所有者或管理员权限。针对单个群的命令应在目标群 @ 机器人发送。应用级设置对当前接收应用生效。

| 命令 | 作用 |
| --- | --- |
| `/config access` | 查看权限摘要，包括应用默认的群 @ 要求。在目标群发送时也显示该群的设置。 |
| `/invite group`、`/config group on` | 授权当前群使用 Bridge。 |
| `/remove group`、`/config group off` | 关闭当前群对 Bridge 的访问。 |
| `/invite all group` | 授权机器人已加入的所有群。可在私聊或群聊发送。 |
| `/config groups [PAGE_OR_CHAT_ID]` | 在管理员私聊中分页浏览机器人所在群，或用完整的 `oc_` ID 打开指定群。群授权和 @ 设置在群管理卡中确认。 |
| `/config mention on`、`/config mention off`、`/config mention default` | 在目标群设置必须 @ 机器人、取消该要求，或继承应用默认值。 |
| `/config mention all`、`/config mention all on`、`/config mention all off` | 在私聊或群聊查看、开启或关闭应用默认的 @ 要求。群自己的覆盖设置仍优先。 |
| `/config mention check`、`/config mention authorize` | 在管理员私聊检查或申请当前应用的 `im:message.group_msg` 权限。按返回的卡片或飞书开放平台指引操作。 |
| `/config access user [PAGE]`、`/config access admin [PAGE]` | 在管理员私聊查看用户或管理员名单，以及启动配置和在线覆盖。 |
| `/config access route ROUTE_ID [PAGE]` | 在管理员私聊查看指定路由的生效用户权限和在线覆盖。 |
| `/invite user USER [USER ...]`、`/remove user USER [USER ...]` | 授予或撤销用户的应用级访问权限。 |
| `/invite admin USER [USER ...]`、`/remove admin USER [USER ...]` | 授予或撤销该应用的管理员角色。 |
| `/invite route ROUTE_ID USER [USER ...]`、`/remove route ROUTE_ID USER [USER ...]` | 授予或撤销用户对指定路由的访问权限。人员名单前须填写完整的路由 ID。 |

将 `USER` 替换为飞书真实用户 @ 或该应用下以 `ou_` 开头的 Open ID。批次内有无效项，或尝试撤销所有者权限时，整批拒绝。应用、群、用户及路由权限分别核对。生产写入授权和 T3 审批由各自的系统管理。

接收无 @ 群消息需要飞书授予 `im:message.group_msg`，且应用已发布 `im.message.receive_v1` 订阅。设置完成后，用一条新的群消息验证投递。 [接收无 @ 的群消息](RUNBOOK.zh-CN.md#接收无--的群消息)

## 接收端诊断与控制

| 命令 | 作用 |
| --- | --- |
| `/doctor` | 查看已知的接收端、连接、路由和排队状态。需要实测模型时可发起回声。 |
| `/doctor --echo [ROUTE_ID]` | 在授权的 `plan`、`approval-required` 路由上运行固定提示词的模型回声。所有者或管理员可在已授权的私聊、群聊或话题发起。当前路由不满足条件时请显式指定路由。群聊或话题会收到受理回执，结果由同一应用私信给发起人。 |
| `/reconnect` | 所有者或管理员重连当前飞书应用。可用于原凭据路径下的密钥轮换。修改配置中的凭据路径或路由后，需要在主机重启接收端。 |
| `/reconnect --wait` | 所有者或管理员暂停该应用的新任务派发，等待在途工作收口后重连。用 `/doctor` 查看进度。 |
| `/reconnect --cancel` | 所有者或管理员取消等待中的重连。 |
| `/ps` | 查看当前应用已登记的接收实例 ID、进程号和启动时间。 |
| `/exit INSTANCE_ID` | 在管理员私聊中，请求单应用接收端在发送回执后关闭。完整实例 ID 从 `/ps` 复制。共用接收端从主机管理。 |
| `/account` | 在管理员私聊中查看接收账号的 App ID、租户及已知连接状态。 |
| `/account change` | 在管理员私聊中获取主机配置工作台的账号换绑步骤。App Secret 保存在主机的私有凭据文件中。 |

模型回声使用独立轮次，期望得到准确的 `OK`。每个应用同时只允许一个待处理或在途回声，同一用户每 30 秒可发起一次，排队和执行合计限时 1 分钟。

## 云文档评论

设置 `commentRoute` 和文档权限后，在受支持的飞书文档评论或回复中 @ 机器人，答复会回到原文档。以下管理命令在机器人会话中发送。

| 命令 | 作用 |
| --- | --- |
| `/doc` | 查看是否已开启云文档评论并获取使用指引。 |
| `/doc tasks [PAGE]` | 在管理员私聊中查看本应用的评论轮次和控制令牌。 |
| `/stop comment:TOKEN` | 在管理员私聊中，使用 `/doc tasks` 返回的令牌请求停止指定的评论轮次。 |
| `/timeout comment:TOKEN`、`/timeout comment:TOKEN MINUTES_OR_MODE` | 在管理员私聊中查看或设置评论线程的空闲超时。可填 1–120 分钟、`off` 关闭或 `default` 继承应用默认值。也可作用于当前轮次。 |

令牌失效时，重新发送 `/doc tasks` 核对目标。 [云文档评论配置](RUNBOOK.zh-CN.md#云文档评论)

## 会议

### 私聊

使用管理员私聊，核对或操作同一私聊管理的会议回执。新入会要求开启 `meetingEnabled`。允许省略会议号的命令需要本人恰好有一场匹配的会议回执。使用 9 位会议号，在 `/meeting ask` 中连续输入这 9 位数字。会议密码应通过会议本身的流程处理。

| 命令 | 作用 |
| --- | --- |
| `/meeting`、`/meeting status` | 查看本人跟踪的会议回执。 |
| `/meeting join NUMBER` | 请求机器人加入会议。收到回执后在参会名单或等候室核对。 |
| `/meeting leave [NUMBER]` | 请求机器人离开已跟踪的会议。 |
| `/meeting confirm-left NUMBER` | 在飞书确认机器人已离开会议及等候室后，结清已保存的回执。 |
| `/meeting transcript [NUMBER]` | 读取有界的字幕摘录。读取不完整时会注明。 |
| `/meeting ask [NUMBER] QUESTION` | 将问题和可用字幕发送到该会议的 T3 会话。结果发回发起私聊。 |
| `/meeting notes [NUMBER]` | 在同一会议上下文整理决定、待办和开放问题。 |
| `/meeting stop [NUMBER]` | 请求停止原会议处理轮次。 |

字幕、问答和纪要需要已授权的 `meetingRoute` 以及已核对的入会回执。可回复会议私聊结果发送 `/result`。 [会议控制与配置](RUNBOOK.zh-CN.md#手动会议控制)

### 会中聊天

开启 `meetingRoomReplies` 后，已授权的参会者可在会议聊天消息开头使用 `@bot` 或 `@BOT_NAME`。每位发言人使用独立的会议会话，完成后将答案公开发到会议聊天。

| 命令 | 作用 |
| --- | --- |
| `@bot /ask QUESTION` | 根据会议字幕提问。 |
| `@bot /notes` | 请求整理会议纪要。 |
| `@bot /stop` | 停止本人当前轮次和此前排队的会中请求。也支持 `@bot stop` 和 `@bot 停止`。 |
