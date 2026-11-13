package alert

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

// Limits for user-defined notification templates. Templates are admin-only;
// the limits catch accidents (a pasted log file, a runaway range) before
// they slow down the alert pipeline.
const (
	MaxTitleTemplateBytes   = 1 << 10
	MaxContentTemplateBytes = 16 << 10
	maxRenderedBytes        = 64 << 10
)

// TemplateStatusFiring is the only notification status today; resolve
// notifications do not exist yet. Templates can branch on .Status so they
// keep working once they do.
const TemplateStatusFiring = "firing"

// ErrTemplateRenderedEmpty means a content template produced only
// whitespace. Live alerts then go out in the default format.
var ErrTemplateRenderedEmpty = errors.New("content template rendered nothing")

// TemplateData is what a notification template can reference. The field
// names are a user-facing contract, documented in
// docs/guides/alert-notification-templates.md.
type TemplateData struct {
	Status     string // "firing"
	Title      string // notification title (the rendered title template, when there is one)
	Severity   string // critical | warning | info
	RuleKey    string
	RuleName   string
	IncidentID uint64
	// IncidentURL links to the incident in the console. Empty unless
	// ONGRID_PUBLIC_URL is set.
	IncidentURL string
	Source      string // rule scope: host | global | monitoring_pipeline | ...
	DedupeKey   string
	Value       string // current value, formatted; empty when unknown
	Threshold   string // empty when the rule has no single threshold
	DeviceID    string
	DeviceName  string // hostname of the device, when resolved
	DeviceIP    string
	RunbookURL  string
	EventCount  uint64
	Labels      map[string]string
	Annotations map[string]string
	FiredAt     time.Time // first firing of the incident
	OccurredAt  time.Time // the firing that triggered this notification
}

// ChannelTemplate is a channel's parsed notification template.
type ChannelTemplate struct {
	channelType string
	format      notify.Format
	title       *template.Template // nil: keep the default title
	content     *template.Template // nil: the sender's default layout
}

// NormalizeMessageFormat validates a channel's message format. Markdown is
// offered where the bot has a Markdown message type; elsewhere the format is
// fixed by the platform (Slack mrkdwn, Telegram text, webhook JSON). The
// stored form of the text format is "".
func NormalizeMessageFormat(channelType, format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", string(notify.FormatText):
		return "", nil
	case string(notify.FormatMarkdown):
		if SupportsMarkdown(channelType) {
			return string(notify.FormatMarkdown), nil
		}
		return "", fmt.Errorf("%w: markdown format is only available for wecom, dingtalk and feishu channels", errs.ErrInvalid)
	default:
		return "", fmt.Errorf("%w: message format must be text or markdown", errs.ErrInvalid)
	}
}

// SupportsMarkdown reports whether a channel type can send Markdown messages.
func SupportsMarkdown(channelType string) bool {
	switch channelType {
	case model.ChannelTypeWeCom, model.ChannelTypeDingTalk, model.ChannelTypeFeishu:
		return true
	}
	return false
}

// ParseChannelTemplate validates a channel's template settings. It returns
// nil without error when the channel keeps the built-in format. With the
// Markdown format and no content template, the built-in Markdown template
// of the channel type applies.
func ParseChannelTemplate(channelType, format, titleSrc, contentSrc string) (*ChannelTemplate, error) {
	f, err := NormalizeMessageFormat(channelType, format)
	if err != nil {
		return nil, err
	}
	markdown := f == string(notify.FormatMarkdown)
	if strings.TrimSpace(titleSrc) == "" && strings.TrimSpace(contentSrc) == "" && !markdown {
		return nil, nil
	}
	if markdown && strings.TrimSpace(contentSrc) == "" {
		contentSrc = DefaultMarkdownTemplate(channelType)
	}
	title, err := parseTemplate("title", titleSrc, MaxTitleTemplateBytes)
	if err != nil {
		return nil, err
	}
	content, err := parseTemplate("content", contentSrc, MaxContentTemplateBytes)
	if err != nil {
		return nil, err
	}
	out := &ChannelTemplate{channelType: channelType, format: notify.FormatText, title: title, content: content}
	if markdown {
		out.format = notify.FormatMarkdown
	}
	return out, nil
}

// Render applies the template to msg, the message the channel would get
// without a template. On error msg is returned unchanged, so callers can
// still deliver the alert in the default format.
func (t *ChannelTemplate) Render(msg notify.Message, data TemplateData) (notify.Message, error) {
	if t == nil {
		return msg, nil
	}
	out := msg
	data.Title = msg.Subject
	if t.title != nil {
		title, err := execute(t.title, data)
		if err != nil {
			return msg, fmt.Errorf("title template: %w", err)
		}
		// A title is one line; keep the default when the template renders
		// nothing (every sender needs a subject).
		if title = strings.Join(strings.Fields(title), " "); title != "" {
			out.Subject = title
		}
	}
	if t.content == nil {
		return out, nil
	}
	data.Title = out.Subject
	content, err := execute(t.content, data)
	if err != nil {
		return msg, fmt.Errorf("content template: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return msg, ErrTemplateRenderedEmpty
	}
	if t.channelType == model.ChannelTypeWebhook && !json.Valid([]byte(content)) {
		return msg, errors.New("content template: the webhook body is not valid JSON")
	}
	out.Rendered = &notify.Rendered{Content: content, Format: t.format}
	return out, nil
}

// templateDataFor builds the template context for one notification. msg is
// the default message (fresh subject, device identity labels); incident is
// the incident row, preferably re-read so value and event count are current.
func templateDataFor(msg notify.Message, incident *model.Incident, publicURL string) TemplateData {
	data := TemplateData{
		Status:     TemplateStatusFiring,
		Title:      msg.Subject,
		Severity:   string(msg.Severity),
		Source:     msg.Source,
		DedupeKey:  msg.DedupeKey,
		OccurredAt: msg.OccurredAt,
		Labels:     map[string]string{},
	}
	if incident != nil {
		data.RuleKey = incident.Rule
		data.RuleName = firstNonEmpty(incident.RuleName, incident.Rule)
		data.IncidentID = incident.ID
		data.IncidentURL = incidentURL(publicURL, incident.ID)
		data.Value = formatTemplateNumber(incident.Value)
		data.Threshold = formatTemplateNumber(incident.Threshold)
		data.RunbookURL = incident.RunbookURL
		data.EventCount = incident.EventCount
		data.FiredAt = incident.FirstFiredAt
		if labels, err := incident.Labels(); err == nil {
			for k, v := range labels {
				data.Labels[k] = v
			}
		}
		if annotations, err := incident.Annotations(); err == nil {
			data.Annotations = annotations
		}
	}
	for k, v := range msg.Labels {
		data.Labels[k] = v
	}
	data.DeviceID = data.Labels["device_id"]
	data.DeviceName = data.Labels["device_hostname"]
	data.DeviceIP = data.Labels["device_ip"]
	if data.Annotations == nil {
		data.Annotations = map[string]string{}
	}
	return data
}

// renderChannelMessage applies ch's template to the default message msg.
// Channels without a template get msg unchanged. A broken template falls back
// to msg and returns the reason, so the alert still goes out.
func renderChannelMessage(ch *model.Channel, msg notify.Message, incident *model.Incident, publicURL string) (notify.Message, error) {
	if ch == nil || !ch.HasTemplate() {
		return msg, nil
	}
	tmpl, err := ParseChannelTemplate(ch.ChannelType, ch.MessageFormat, ch.TitleTemplateText(), ch.ContentTemplateText())
	if err != nil {
		return msg, err
	}
	return tmpl.Render(msg, templateDataFor(msg, incident, publicURL))
}

// SampleTemplateData is a representative firing alert for template previews
// and channel tests, in English for locale "en" and Chinese otherwise. Its
// title is marked as a test so nobody mistakes it for a real incident.
func SampleTemplateData(locale, publicURL string, now time.Time) TemplateData {
	value, threshold := 93.5, 90.0
	incident := &model.Incident{
		ID:           1024,
		Rule:         "host_cpu_high",
		RuleName:     "主机 CPU 使用率过高",
		Value:        &value,
		Threshold:    &threshold,
		RunbookURL:   "https://wiki.example.com/runbooks/host-cpu",
		EventCount:   3,
		FirstFiredAt: now.Add(-5 * time.Minute),
		LabelsJSON:   `{"rule":"host_cpu_high","instance":"10.0.0.12:9100","job":"node","device_id":"42"}`,
	}
	title := "[测试] host_cpu_high: CPU 使用率 93.5% 超过 90%（device=web-01 (10.0.0.12)）"
	if strings.EqualFold(strings.TrimSpace(locale), "en") {
		incident.RuleName = "High host CPU usage"
		title = "[test] host_cpu_high: CPU usage 93.5% above 90% (device=web-01 (10.0.0.12))"
	}
	msg := notify.Message{
		Subject:    title,
		Severity:   notify.SeverityCritical,
		Source:     model.RuleScopeHost,
		DedupeKey:  "pipeline:host_cpu_high:device_id=42",
		OccurredAt: now,
		Labels: map[string]string{
			"rule":            incident.Rule,
			"incident_id":     strconv.FormatUint(incident.ID, 10),
			"device_id":       "42",
			"device_hostname": "web-01",
			"device_ip":       "10.0.0.12",
		},
	}
	return templateDataFor(msg, incident, publicURL)
}

// SampleMessage is the default message matching sample data, i.e. what a
// channel without a template would receive for the sample alert.
func SampleMessage(data TemplateData) notify.Message {
	labels := make(map[string]string, len(data.Labels))
	for k, v := range data.Labels {
		labels[k] = v
	}
	return notify.Message{
		Subject:    data.Title,
		Severity:   notify.Severity(data.Severity),
		Source:     data.Source,
		DedupeKey:  data.DedupeKey,
		OccurredAt: data.OccurredAt,
		Labels:     labels,
	}
}

// incidentURL is the console page of an incident, or "" without a public URL.
func incidentURL(publicURL string, incidentID uint64) string {
	base := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if base == "" || incidentID == 0 {
		return ""
	}
	return base + "/alerts/incidents/" + strconv.FormatUint(incidentID, 10)
}

// formatTemplateNumber renders a metric value for people: integers without
// decimals, others with up to two decimals, tiny values with three
// significant digits.
func formatTemplateNumber(v *float64) string {
	if v == nil {
		return ""
	}
	x := *v
	switch {
	case math.IsNaN(x) || math.IsInf(x, 0):
		return strconv.FormatFloat(x, 'g', -1, 64)
	case x == math.Trunc(x) && math.Abs(x) < 1e15:
		return strconv.FormatFloat(x, 'f', 0, 64)
	case math.Abs(x) >= 0.01:
		s := strconv.FormatFloat(x, 'f', 2, 64)
		return strings.TrimRight(strings.TrimRight(s, "0"), ".")
	default:
		return strconv.FormatFloat(x, 'g', 3, 64)
	}
}

var templateFuncs = template.FuncMap{
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"trim":  strings.TrimSpace,
	// replace / contains / default take the subject last, like Sprig, so
	// they work in pipelines: {{.Title | replace "_" " "}}.
	"replace":  func(old, repl, s string) string { return strings.ReplaceAll(s, old, repl) },
	"contains": func(substr, s string) bool { return strings.Contains(s, substr) },
	"truncate": truncateTemplateText,
	"default":  defaultValue,
	"json":     toJSON,
	// date formats in the manager's local time zone (the TZ environment
	// variable); dateInZone takes an IANA zone such as "Asia/Shanghai".
	"date": func(layout string, t time.Time) string { return t.In(time.Local).Format(layout) },
	"dateInZone": func(layout string, t time.Time, zone string) (string, error) {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return "", err
		}
		return t.In(loc).Format(layout), nil
	},
}

func truncateTemplateText(n int, s string) string {
	runes := []rune(s)
	if n < 0 || len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func defaultValue(fallback, v any) any {
	if v == nil {
		return fallback
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.String:
		if rv.Len() == 0 {
			return fallback
		}
	default:
		if rv.IsZero() {
			return fallback
		}
	}
	return v
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseTemplate(name, src string, maxBytes int) (*template.Template, error) {
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}
	if len(src) > maxBytes {
		return nil, fmt.Errorf("%w: %s template is longer than %d bytes", errs.ErrInvalid, name, maxBytes)
	}
	t, err := template.New(name).Option("missingkey=zero").Funcs(templateFuncs).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %s template: %w", errs.ErrInvalid, name, err)
	}
	if err := checkTemplateActions(t); err != nil {
		return nil, fmt.Errorf("%w: %s template: %w", errs.ErrInvalid, name, err)
	}
	return t, nil
}

// checkTemplateActions rejects the constructs that make execution time
// unbounded: nested templates ({{define}}, {{block}}, {{template}}) can
// recurse, and {{range}} over a number literal loops without touching data.
func checkTemplateActions(t *template.Template) error {
	if len(t.Templates()) > 1 {
		return errors.New("{{define}} and {{block}} are not supported")
	}
	if t.Tree == nil {
		return nil
	}
	return walkTemplateList(t.Tree.Root)
}

func walkTemplateList(list *parse.ListNode) error {
	if list == nil {
		return nil
	}
	for _, node := range list.Nodes {
		if err := walkTemplateNode(node); err != nil {
			return err
		}
	}
	return nil
}

func walkTemplateNode(node parse.Node) error {
	switch n := node.(type) {
	case *parse.TemplateNode:
		return errors.New("{{template}} is not supported")
	case *parse.RangeNode:
		if rangesOverNumber(n.Pipe) {
			return errors.New("{{range}} over a number is not supported")
		}
		return walkBranch(n.List, n.ElseList)
	case *parse.IfNode:
		return walkBranch(n.List, n.ElseList)
	case *parse.WithNode:
		return walkBranch(n.List, n.ElseList)
	case *parse.ListNode:
		return walkTemplateList(n)
	}
	return nil
}

func walkBranch(list, elseList *parse.ListNode) error {
	if err := walkTemplateList(list); err != nil {
		return err
	}
	return walkTemplateList(elseList)
}

func rangesOverNumber(pipe *parse.PipeNode) bool {
	if pipe == nil || len(pipe.Cmds) != 1 || len(pipe.Cmds[0].Args) != 1 {
		return false
	}
	_, isNumber := pipe.Cmds[0].Args[0].(*parse.NumberNode)
	return isNumber
}

var errRenderTooLarge = fmt.Errorf("rendered text exceeds %d bytes", maxRenderedBytes)

// boundedBuffer stops template execution once the output passes
// maxRenderedBytes; text/template aborts on the first write error.
type boundedBuffer struct {
	b strings.Builder
}

func (w *boundedBuffer) Write(p []byte) (int, error) {
	if w.b.Len()+len(p) > maxRenderedBytes {
		return 0, errRenderTooLarge
	}
	return w.b.Write(p)
}

func execute(t *template.Template, data TemplateData) (string, error) {
	var out boundedBuffer
	if err := t.Execute(&out, data); err != nil {
		return "", err
	}
	return out.b.String(), nil
}
