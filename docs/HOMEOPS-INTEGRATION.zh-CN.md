# HomeOps 可选集成

[English](HOMEOPS-INTEGRATION.md) | 简体中文

HomeOps 是面向自托管基础设施的独立运维系统，负责健康检查、故障与恢复跟踪、维护协调和报告。HomeOps 提供运维数据和协调状态，T3 Bridge 负责核验事件，并按授权将事件、调查或报告接入已有的飞书与 T3 流程。

普通桥接实例使用单路由配置即可，无需 HomeOps。启用集成时，从 [可选适配示例](../config/homeops.example.json) 开始，并由实例运维方配置检查项、协调命令、签名源和权限。

- **维护**。带 `checks` 的目标需配置绝对路径的 `coordinationCommand`。Bridge 读取协调状态并按实际目标检查项限制派发或生产写入。真实执行线程仍须遵守部署侧的登记和写权限校验。
- **事件**。`health` 定义固定来源、预期节点、本地源地址和私有 HMAC 文件。Bridge 使用 `X-HomeOps-Signature` 校验后再读取 `homeops.eventbridge/v1` 事件。通过 `eventChats`、`eventRoutes` 与目标 ACL 确定允许关联的范围，告警调查使用只读上下文。
- **自动调查**。`autoInvestigations.enabled` 默认关闭。接管旧自动调查前先确认旧调度器停止、排空任务和报告，再按现有 CLI `-prepare-auto-baseline` 准备首次状态。早于基线的历史事件保留既有去重身份。
- **报告**。`-submit-report` 从标准输入接收绑定 task、request 和 event 的报告。报告源、输出和跨主机复制由部署方明确配置。业务报告、探针恢复和 T3 运行结束分别记录。

接替现有实例前，在测试环境核对账号、路由身份、状态格式、未结任务和回退步骤。
