# 自定义告警通知模板

每个通知渠道（**设置 → 通知**）都可以单独配置消息模板，决定告警通知的标题和正文。模板使用 Go [`text/template`](https://pkg.go.dev/text/template) 语法，只作用于告警通知；报表、工作流通知节点和助手的 `send_notification` 仍使用原来的格式。三个字段都留空时，渠道保持内置格式。

## 字段

| 字段 | 作用 |
| --- | --- |
| 消息格式 | 企业微信、钉钉、飞书可选「文本」或「Markdown」。Markdown 分别发送企业微信 `markdown` 消息、钉钉 `markdown` 消息、飞书卡片（标题栏按级别着色）。其他渠道的格式固定：Slack 为 mrkdwn，Telegram 为纯文本，Webhook 为 JSON。 |
| 标题模板 | 单行，换行会被合并成空格。用于有标题位的平台（钉钉 Markdown 标题、飞书卡片标题栏、Slack 标题），也是内置文本格式的第一行。渲染为空时沿用默认标题。 |
| 内容模板 | 完整正文。Webhook 渠道的内容模板是**整个 POST 请求体**，必须渲染成合法 JSON。选择 Markdown 但内容模板留空时，使用内置的 Markdown 模板。 |

编辑器里的「填入示例模板」会按渠道类型和格式填入一份可直接保存的模板，「预览」用一条示例告警在服务端渲染，不会保存或发送。保存时模板会先用示例告警渲染一次，语法错误、引用不存在的字段、Webhook 请求体不是合法 JSON 都会直接报错。渠道的「测试」按钮在配置了模板时发送渲染后的示例告警（标题带「[测试]」）。

## 变量

| 变量 | 说明 |
| --- | --- |
| `.Title` | 通知标题。在标题模板里是默认标题；在内容模板里是最终标题（设置了标题模板时为其渲染结果） |
| `.Severity` | `critical` / `warning` / `info` |
| `.Status` | 通知类型，目前固定为 `firing` |
| `.RuleName` / `.RuleKey` | 规则名称 / 规则 key |
| `.Value` / `.Threshold` | 当前值 / 阈值，已格式化（整数不带小数，其余最多两位小数）；未知时为空 |
| `.DeviceName` / `.DeviceIP` / `.DeviceID` | 主机名 / IP / 设备 ID，无主机信息时为空 |
| `.Labels.<名称>` | 告警标签，如 `.Labels.instance`、`.Labels.mountpoint`；不存在的标签渲染为空 |
| `.Annotations.<名称>` | 告警注解 |
| `.Source` | 规则作用域，如 `host`、`global` |
| `.EventCount` | 该告警累计触发次数 |
| `.FiredAt` / `.OccurredAt` | 首次触发时间 / 本次触发时间（`time.Time`） |
| `.IncidentURL` | 告警详情页链接；需要配置 `ONGRID_PUBLIC_URL`，否则为空 |
| `.RunbookURL` | 规则的 Runbook 链接 |
| `.IncidentID` / `.DedupeKey` | 告警 ID / 去重 key |

## 函数

除 Go 模板内置的 `eq`、`if`、`range`、`printf`、`len` 等，还可以使用：

| 函数 | 示例 |
| --- | --- |
| `upper` / `lower` / `trim` | `{{upper .Severity}}` |
| `replace 旧 新 字符串` | `{{.RuleKey \| replace "_" " "}}` |
| `contains 子串 字符串` | `{{if contains "prod" .Labels.env}}…{{end}}` |
| `truncate 长度 字符串` | `{{truncate 50 .Title}}`，超出部分以 `…` 结尾 |
| `default 备选 值` | `{{default "-" .Value}}` |
| `json 值` | `{{json .Title}}`，输出带引号并转义的 JSON，Webhook 请求体里的字符串都应这样写 |
| `date 格式 时间` | `{{date "2006-01-02 15:04:05" .OccurredAt}}`，使用 Manager 进程的时区（`TZ` 环境变量） |
| `dateInZone 格式 时间 时区` | `{{dateInZone "2006-01-02 15:04" .OccurredAt "Asia/Shanghai"}}` |

为保证告警链路的执行时间可控，不支持 `{{define}}`、`{{block}}`、`{{template}}` 和对数字字面量的 `{{range}}`。标题模板最长 1 KiB，内容模板最长 16 KiB，渲染结果最长 64 KiB。

## 示例

企业微信 Markdown（支持 `<font color="info|comment|warning">`）：

```
**{{.Title}}**
> 级别：<font color="warning">{{upper .Severity}}</font>
> 主机：{{.DeviceName}}（{{.DeviceIP}}）
> 当前值：{{default "-" .Value}}
> 时间：{{dateInZone "2006-01-02 15:04:05" .OccurredAt "Asia/Shanghai"}}
{{- if .IncidentURL}}

[查看告警详情]({{.IncidentURL}})
{{- end}}
```

Webhook JSON 请求体：

```
{
  "title": {{json .Title}},
  "severity": {{json .Severity}},
  "host": {{json .DeviceName}},
  "value": {{json .Value}},
  "labels": {{json .Labels}},
  "fired_at": {{json .FiredAt}}
}
```

`{{-` 和 `-}}` 会去掉相邻的空白和换行，用于控制可选行。

## 发送时的行为

- 模板在每次发送（包括失败重试）时用告警的最新数据渲染。
- 发送时渲染失败（例如 `dateInZone` 用了不存在的时区）不会丢失告警：该渠道改发内置格式，告警事件里的投递记录会注明 `template failed, sent the default format` 和原因。
- 平台的长度限制仍然有效：企业微信群机器人文本 2048 字节、Markdown 4096 字节，超出部分会被截断。企业微信、钉钉、飞书机器人在 HTTP 200 中返回的错误码（签名错误、内容过长等）会记为投递失败并进入重试。
