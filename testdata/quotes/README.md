# 飞书 GET 引用夹具

所有内容为合成测试数据。响应采用 Go oapi SDK 的 `im.GetMessageResp` 结构，`body.content` 为 JSON 字符串。fixture 分别覆盖 post、interactive、Card 2.0 预览及合并转发。引用解析只读取支持的可见内容，已知任务卡同时可以通过持久卡片身份续聊。
