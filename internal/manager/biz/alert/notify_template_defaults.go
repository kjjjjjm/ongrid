package alert

import (
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

// DefaultMarkdownTemplate is the content template a Markdown channel uses
// when it has none of its own. The Chinese wording matches the platforms
// that offer Markdown (WeCom, DingTalk, Feishu).
func DefaultMarkdownTemplate(channelType string) string {
	return StarterTemplate(channelType, string(notify.FormatMarkdown), "zh")
}

// StarterTemplate is an editable example content template for a channel type
// and format, in English for locale "en" and Chinese otherwise. The settings
// page offers it as a starting point; for Markdown in Chinese it equals the
// built-in default.
func StarterTemplate(channelType, format, locale string) string {
	en := strings.EqualFold(strings.TrimSpace(locale), "en")
	markdown := strings.EqualFold(strings.TrimSpace(format), string(notify.FormatMarkdown)) && SupportsMarkdown(channelType)
	switch {
	case channelType == model.ChannelTypeWebhook:
		return webhookStarter
	case channelType == model.ChannelTypeSlack:
		return pick(en, slackStarterEN, slackStarterZH)
	case markdown && channelType == model.ChannelTypeWeCom:
		return pick(en, wecomMarkdownEN, wecomMarkdownZH)
	case markdown && channelType == model.ChannelTypeDingTalk:
		return pick(en, dingtalkMarkdownEN, dingtalkMarkdownZH)
	case markdown && channelType == model.ChannelTypeFeishu:
		return pick(en, feishuMarkdownEN, feishuMarkdownZH)
	default:
		return pick(en, textStarterEN, textStarterZH)
	}
}

func pick(en bool, enText, zhText string) string {
	if en {
		return enText
	}
	return zhText
}

const textStarterZH = `【{{upper .Severity}}】{{.Title}}
规则：{{.RuleName}}
{{- if .DeviceName}}
主机：{{.DeviceName}}{{if .DeviceIP}}（{{.DeviceIP}}）{{end}}
{{- end}}
{{- if .Value}}
当前值：{{.Value}}
{{- end}}
时间：{{date "2006-01-02 15:04:05" .OccurredAt}}
{{- if .IncidentURL}}
详情：{{.IncidentURL}}
{{- end}}`

const textStarterEN = `[{{upper .Severity}}] {{.Title}}
Rule: {{.RuleName}}
{{- if .DeviceName}}
Host: {{.DeviceName}}{{if .DeviceIP}} ({{.DeviceIP}}){{end}}
{{- end}}
{{- if .Value}}
Value: {{.Value}}
{{- end}}
Time: {{date "2006-01-02 15:04:05 MST" .OccurredAt}}
{{- if .IncidentURL}}
Details: {{.IncidentURL}}
{{- end}}`

// WeCom group-bot Markdown supports <font color="info|comment|warning">.
const wecomMarkdownZH = `**{{.Title}}**
> 级别：<font color="{{if eq .Severity "info"}}info{{else}}warning{{end}}">{{upper .Severity}}</font>
> 规则：{{.RuleName}}
{{- if .DeviceName}}
> 主机：{{.DeviceName}}{{if .DeviceIP}}（{{.DeviceIP}}）{{end}}
{{- end}}
{{- if .Value}}
> 当前值：{{.Value}}
{{- end}}
> 时间：{{date "2006-01-02 15:04:05" .OccurredAt}}
{{- if .IncidentURL}}

[查看告警详情]({{.IncidentURL}})
{{- end}}`

const wecomMarkdownEN = `**{{.Title}}**
> Severity: <font color="{{if eq .Severity "info"}}info{{else}}warning{{end}}">{{upper .Severity}}</font>
> Rule: {{.RuleName}}
{{- if .DeviceName}}
> Host: {{.DeviceName}}{{if .DeviceIP}} ({{.DeviceIP}}){{end}}
{{- end}}
{{- if .Value}}
> Value: {{.Value}}
{{- end}}
> Time: {{date "2006-01-02 15:04:05 MST" .OccurredAt}}
{{- if .IncidentURL}}

[Open incident]({{.IncidentURL}})
{{- end}}`

// DingTalk shows the markdown title only in previews, so the body repeats it.
const dingtalkMarkdownZH = `### {{.Title}}
- 级别：**{{upper .Severity}}**
- 规则：{{.RuleName}}
{{- if .DeviceName}}
- 主机：{{.DeviceName}}{{if .DeviceIP}}（{{.DeviceIP}}）{{end}}
{{- end}}
{{- if .Value}}
- 当前值：{{.Value}}
{{- end}}
- 时间：{{date "2006-01-02 15:04:05" .OccurredAt}}
{{- if .IncidentURL}}

[查看告警详情]({{.IncidentURL}})
{{- end}}`

const dingtalkMarkdownEN = `### {{.Title}}
- Severity: **{{upper .Severity}}**
- Rule: {{.RuleName}}
{{- if .DeviceName}}
- Host: {{.DeviceName}}{{if .DeviceIP}} ({{.DeviceIP}}){{end}}
{{- end}}
{{- if .Value}}
- Value: {{.Value}}
{{- end}}
- Time: {{date "2006-01-02 15:04:05 MST" .OccurredAt}}
{{- if .IncidentURL}}

[Open incident]({{.IncidentURL}})
{{- end}}`

// Feishu shows the title in the card header, so the body leaves it out.
const feishuMarkdownZH = `**级别**：{{upper .Severity}}
**规则**：{{.RuleName}}
{{- if .DeviceName}}
**主机**：{{.DeviceName}}{{if .DeviceIP}}（{{.DeviceIP}}）{{end}}
{{- end}}
{{- if .Value}}
**当前值**：{{.Value}}
{{- end}}
**时间**：{{date "2006-01-02 15:04:05" .OccurredAt}}
{{- if .IncidentURL}}
[查看告警详情]({{.IncidentURL}})
{{- end}}`

const feishuMarkdownEN = `**Severity**: {{upper .Severity}}
**Rule**: {{.RuleName}}
{{- if .DeviceName}}
**Host**: {{.DeviceName}}{{if .DeviceIP}} ({{.DeviceIP}}){{end}}
{{- end}}
{{- if .Value}}
**Value**: {{.Value}}
{{- end}}
**Time**: {{date "2006-01-02 15:04:05 MST" .OccurredAt}}
{{- if .IncidentURL}}
[Open incident]({{.IncidentURL}})
{{- end}}`

// Slack renders the content as attachment mrkdwn under the title.
const slackStarterZH = `*{{upper .Severity}}* · {{.RuleName}}
{{- if .DeviceName}}
*主机：* {{.DeviceName}}{{if .DeviceIP}}（{{.DeviceIP}}）{{end}}
{{- end}}
{{- if .Value}}
*当前值：* {{.Value}}
{{- end}}
*时间：* {{date "2006-01-02 15:04:05" .OccurredAt}}
{{- if .IncidentURL}}
<{{.IncidentURL}}|查看告警详情>
{{- end}}`

const slackStarterEN = `*{{upper .Severity}}* · {{.RuleName}}
{{- if .DeviceName}}
*Host:* {{.DeviceName}}{{if .DeviceIP}} ({{.DeviceIP}}){{end}}
{{- end}}
{{- if .Value}}
*Value:* {{.Value}}
{{- end}}
*Time:* {{date "2006-01-02 15:04:05 MST" .OccurredAt}}
{{- if .IncidentURL}}
<{{.IncidentURL}}|Open incident>
{{- end}}`

// The webhook content template is the whole JSON request body; json quotes
// and escapes each value.
const webhookStarter = `{
  "status": {{json .Status}},
  "title": {{json .Title}},
  "severity": {{json .Severity}},
  "rule": {{json .RuleKey}},
  "rule_name": {{json .RuleName}},
  "incident_id": {{.IncidentID}},
  "incident_url": {{json .IncidentURL}},
  "value": {{json .Value}},
  "device": {{json .DeviceName}},
  "labels": {{json .Labels}},
  "fired_at": {{json .FiredAt}},
  "occurred_at": {{json .OccurredAt}}
}`
