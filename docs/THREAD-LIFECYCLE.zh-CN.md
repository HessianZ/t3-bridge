# T3 线程标题与列表收口

[English](THREAD-LIFECYCLE.md) | 简体中文

## 产品规则

新空闲线程的标题在launch前持久保存，再随原稳定clientRequestId提交。普通标题来自首轮用户的规范化可见文字，先用RoutingText去bot mention；合并消息使用Parts中的实际文字意图，纯附件用“附件资料”。不从Quote、机器上下文、资源key或隐藏日志取标题；沿用display脱敏、另省略URL与私钥标记。普通意图最多60字符。可信事件只用已验证EventRecords的node/check/failure_start，按“自动调查/告警解读/复核”区分用途，上海时间到分钟；总标题不超过80字符。后续轮不改首轮标题。

只遍历私有Store已有Task，不扫描T3所有线程。要求原Source派生TaskID、接收App绑定、当前目标执行策略与用户ACL符合，再用Preflight验证环境/项目/provider→driver/模型/选项/权限，并Verify实际线程配置。原生线程须来自MCP且无parent。最新 Run 须是当前轮或历史轮保存的原 Run 或已核实的委派收口 Run。后来独立提交的 Run、已归档线程及缺失证据都会阻止整理。新线程不再补rename；迁移旧线程时仅在标题精确等于原`HomeOps · stable(taskID,"open")[:8]`时rename，已有可读/手工标题保留。

T3 等待委派工作收口时可能返回整个线程最近的结果 Run。Bridge 使用公开活动视图核对从原 Run 起连续的完成通知。单条和合并通知按名称、数量和不同任务 ID 对应到同一发起 Run。已核实送达的历史任务可以排除，名称可在后续轮复用。有独立输入的 Run 不参与归属。若已出现较新的独立轮次，回取原收口答复还需核实已归属的全部委派任务。确认后单独保存收口 Run ID，消息和执行回执继续使用原 Run ID。匹配多个来源、文本截断、名称分隔歧义或证据不足时继续待核对。 [T3 结果选择](https://github.com/pingdotgg/t3code/blob/7d32075e3757ded707914e2450c3e392a9665765/packages/provider-core/src/server/subagentProjection.ts#L288-L329) · [T3 完成通知](https://github.com/pingdotgg/t3code/blob/7d32075e3757ded707914e2450c3e392a9665765/packages/provider-core/src/server/notification.ts#L172-L252)

以下任务可自动settle：一次性automatic/event/review（即使仍在Current）、确认收口的停止/失败、/new/reset后不再当前的普通会话，以及成功项目切换时明确离开的旧项目会话。Current按同用户/chat/topic/route/目标/项目精确键判断；切项目只给离开的旧Task记Detached，不删它或其它项目的Current。普通当前会话保留open；旧State没有明确Current映射时保守保留。/resume或显式续聊重选原Task时清Detached，保留同Thread和所有轮次。

settle前必须同时满足：

- Bridge为completed/failed/stopped且本轮最终卡NotifyHash已确认、没有NotificationIssue、未提交的Pending、排队续聊、开放表单或真实待办；blocked/clarify和仍跟踪收口的任务不收起。
- **所有phase**均拒绝NeedsReconcile或StopRequested但StopSent=false；成功Stop ACK也不代替原生静默。仍带未确认 Stop 标记的失败任务会等待。
- 最新原生run和所读recent runs终态，activeRunId明确null，无pending审批/问题、background工作或thread wait；已知子线程也逐个只读确认无active/pending并终态。thread状态idle只有在已记录run终态时才视作静默。业务phase不冒充原生静默，事件物理恢复不是UI settle前提。
- 网络核对后、organize前再次检查当前本地停止/队列/通知条件。公开API没有跨进程CAS：其它客户端恰在read与organize之间入队仍由T3正式语义处理；不以本地锁冒充远端事务。

## 回执、续聊与故障

Task.threadMeta仅存标题版本状态、每request已settled回执、项目Detached、尝试/下次检查时间和固定低敏Issue。元数据更新不改Source/Thread/Run/Request/结果/停止/baseline，不影响卡片hash。已settled原生线程直接认领回执；同一request已确认后不再次强制settle，用户手动unsettle会保留。新轮request可再次独立收口。已有可读标题按人工标题处理。

后台沿原2秒poll，每次最多处理2个Task、总metadata context最多10秒；正常100ms合并输入唤醒不做metadata。每个request最多3次失败尝试，间隔至少30秒，重启先read回读，不重放业务Send/Stop。运行/待审批等明确未静默状态只按30秒再检查。确认回执的快速路径锁内检查，不复制或散列全文历史；没有逐Task全State snapshot。

恢复同线程发新一轮前，metadata使用**独立2秒context**，若实际settled则unsettle并回读；无新顶层线程，不自动恢复停止/拒绝任务。可选metadata失败保存Issue（自己的/status可见），业务仍按原持久Send fence只尝试一次；若T3最终拒绝Send或ACK未知，则沿原needs_reconcile只读核对，绝不因metadata失败重发。验收时应在独立实例验证展开后续聊到原线程，并核对原轮结果和新 run。

## T3 API

- `t3_thread_launch`：原clientRequestId/受控执行身份不变，只将title换为已落盘可读标题。
- `t3_thread_update(action=rename,title,environmentId,threadId,clientRequestId)`：稳定`title-v1`请求身份，校验ACK并read回读；不调用regenerate_title，不触发LLM。
- `t3_thread_organize(action=settle|unsettle,environmentId,threadId)`：不调用archive/delete；先保存尝试，未知ACK后先read确认。
- 复用`t3_thread_read`、只读`t3_thread_configure`及原Preflight环境/项目/capabilities读取；不新增scope、依赖或模型请求。
