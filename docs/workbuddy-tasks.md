# WorkBuddy 任务中心（一键完成 / 活跃上报 / 保活）

WorkBuddy 的成长中心（任务中心）把积分奖励挂在一批「成长任务」上：对话 5 次、体验资料库、
换主题、召唤专家……每一项都是一个计数器，计数器只认 WorkBuddy 自己客户端发的埋点
（`POST /v2/report`）。所以这里的实现分三层：

| 层 | 文件 | 作用 |
| --- | --- | --- |
| 任务读写 | `internal/gateway/workbuddy_growth.go` | 列表 / 报名 / 领奖（含小程序口径与 Web 域降级） |
| 埋点上报 | `internal/gateway/workbuddy_report.go` | 桌面 / Web / 小程序三族事件与设备指纹 |
| 动作执行 | `internal/gateway/workbuddy_autotask.go` | 动作表、状态机、一键全量、补领清扫 |
| 控制台 | `internal/gateway/workbuddy_tasks_api.go` | HTTP 接口、保活、每日调度 |

## 控制台接口

全部挂在 Provider 下，与签到、额度同一套鉴权（`requireProviderAccessForUser`）。

```
GET  /__providers/{id}/workbuddy-oauth/tasks
     任务列表 + 自动化开关 + 最近一次执行（打开面板一次请求即可渲染）
POST /__providers/{id}/workbuddy-oauth/tasks/auto
     {"taskCodes":["chat_5"]} 或 {} 跑整张动作表；返回 {"runId":...}，执行是异步的
GET  /__providers/{id}/workbuddy-oauth/tasks/run?runId=...
     轮询执行进度（不打上游）。一轮几分钟：小程序对话任务按真人节奏 ~45s 一条
POST /__providers/{id}/workbuddy-oauth/activity
     只发一条 chat_request_send：连登天数与 first_buddy 的门槛都看它
POST /__providers/{id}/workbuddy-oauth/keepalive
     续期令牌 + 活跃上报 + 签到 + 补领（每一步独立，失败互不阻塞）
POST /__providers/{id}/workbuddy-oauth/automation
     {"dailyTasks":true,"dailyKeepalive":true,"hour":9}
```

自动化设置按 Provider 存在 `settings` 表（键 `workbuddy_automation:<providerID>`），
与签到开关同样的理由：Provider 的部分更新不能把它们重置掉。

## 完成判据

`status` 是唯一的完成证据：

| status | 含义 |
| --- | --- |
| `done` | 服务端确认已领奖（或此前已领取） |
| `claim_pending` | 进度已达标，但领奖接口失败；下次补领 |
| `awaiting_progress` | 动作已执行，服务端还没计分（异步 + 可能回滚） |
| `skipped` | 该账号没有这个任务 / 未解锁 / 没有可用动作 |
| `error` | 读任务或执行动作失败 |

「事件发出去了」永远不等于完成：计分是异步的，小程序任务连发的事件还会被回滚
（见下文节奏）。已达标未领的任务不会再跑动作，直接领奖；本表没有动作的码走
「只领奖」兜底（`workBuddyQueuedActionFor`），否则它们永远卡着。

## 动作表

顺序即执行顺序（被依赖的在前）。`Attempt` 表示该链路的计分口径在上游未被纯 API
验证过，`awaiting_progress` 属于预期答案：

| 任务码 | 动作 | 备注 |
| --- | --- | --- |
| `chat_5` | 按差额上报 chat_request_send | 间隔 1.05s |
| `first_buddy` | 活跃上报 → 同意协议 → 领养 | 进度满也仍要走领养链 |
| `RichMeow_Chat` | 桌面完整对话事件链（6 事件） | |
| `Buddy_App` / `Buddy_App_QQ` | 进入 Buddy 应用五连事件 | 一条链满足两项 |
| `automation_1` | `automated_task_create_suc` | |
| `Library_read` | Web 域 `web_element_click` | |
| `template_5` | 模板事件组 ×5 | |
| `playbook_prompt` | 灵感案例 Prompt 事件组 | |
| `Hp_Appearance` | 应用主题 + `appearance_skin_apply` | 只设主题不计分 |
| `skill_1` | 真实对话 + `skill_info` | Attempt |
| `expert_5` / `Expert_team_use_3` | 专家市场召唤 + 使用链 | Attempt，6s 间隔 |
| `Expert_lighthouse` / `WorkBuddy_Valorant` / `expert_use_*` | 指定专家召唤 + 使用链（mode=LOCAL） | Attempt |
| `Sequential_Tasks_1/3/6` | 小程序对话事件按差额上报 | 真人节奏 ~45s，连发会被回滚 |
| `Sequential_Tasks_2` | 小程序专家使用事件 | 需要市场里真实的 ex_ id |
| `Sequential_Tasks_4` | 定时任务创建事件（复用 PC 口径） | |
| `Sequential_Tasks_5` | 带 `glm-5.2` 的小程序对话事件 | |
| `Sequential_Tasks_7` | 小程序灵感事件组 | |

国际版（WorkBuddy AI）只读：列表能读，写入一律被拒，面板上按钮直接禁用。

## 每日调度

`StartWorkBuddyAutomation` 每 5 分钟看一次：Provider 没被禁用、开了任一开关、
北京日期还没跑过、且到了设定时刻，就起一次后台任务。日期在开工前先落库，异常中断
不会在当天反复重跑。一轮 = 保活（可选）+ 一键完成（可选），结果写进
`lastRunDay / lastRunAt / lastSummary` 显示在面板上。

## 节奏与限流

- 连续上报间隔 1.05s；成长域 429 会按 15s/30s 各重试一次（上限 60s）；
- 小程序对话事件 45s + 抖动，因为上游对「5 次有效对话」有反作弊校验；
- 一轮执行只读两次任务列表（默认口径 + 小程序口径），逐项执行复用这份快照，
  避免「每个任务一次列表」把限流窗口打满。
