package alert

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

func strPtr(s string) *string { return &s }

var templateTestNow = time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC)

func sampleForTest() (notify.Message, TemplateData) {
	data := SampleTemplateData("zh", "https://ongrid.example.com/", templateTestNow)
	return SampleMessage(data), data
}

func mustParse(t *testing.T, channelType, format, title, content string) *ChannelTemplate {
	t.Helper()
	tmpl, err := ParseChannelTemplate(channelType, format, title, content)
	if err != nil {
		t.Fatalf("ParseChannelTemplate: %v", err)
	}
	return tmpl
}

func TestParseChannelTemplate_BuiltInFormatNeedsNoTemplate(t *testing.T) {
	tmpl, err := ParseChannelTemplate(model.ChannelTypeWeCom, "", "  ", "")
	if err != nil || tmpl != nil {
		t.Fatalf("got %v, %v; want no template", tmpl, err)
	}
}

func TestParseChannelTemplate_RejectsUnsafeOrInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name, channelType, format, title, content string
	}{
		{"markdown on slack", model.ChannelTypeSlack, "markdown", "", ""},
		{"markdown on webhook", model.ChannelTypeWebhook, "markdown", "", "{}"},
		{"unknown format", model.ChannelTypeWeCom, "html", "", ""},
		{"syntax error", model.ChannelTypeWeCom, "", "{{.Title", ""},
		{"unknown function", model.ChannelTypeWeCom, "", "", "{{shell .Title}}"},
		{"define", model.ChannelTypeWeCom, "", "", `{{define "x"}}a{{end}}b`},
		{"template call", model.ChannelTypeWeCom, "", "", `{{template "x" .}}`},
		{"block", model.ChannelTypeWeCom, "", "", `{{block "x" .}}a{{end}}`},
		{"range over a number", model.ChannelTypeWeCom, "", "", `{{range 100000000}}{{end}}`},
		{"nested range over a number", model.ChannelTypeWeCom, "", "", `{{if .Title}}{{range $i := 9}}x{{end}}{{end}}`},
		{"title too long", model.ChannelTypeWeCom, "", strings.Repeat("x", MaxTitleTemplateBytes+1), ""},
		{"content too long", model.ChannelTypeWeCom, "", "", strings.Repeat("x", MaxContentTemplateBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseChannelTemplate(tc.channelType, tc.format, tc.title, tc.content)
			if !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestRender_TitleTemplateReplacesTheSubjectOnly(t *testing.T) {
	msg, data := sampleForTest()
	tmpl := mustParse(t, model.ChannelTypeDingTalk, "", "  【{{upper .Severity}}】\n{{.RuleName}}  ", "")

	out, err := tmpl.Render(msg, data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out.Subject != "【CRITICAL】 主机 CPU 使用率过高" {
		t.Fatalf("subject = %q, want one trimmed line", out.Subject)
	}
	if out.Rendered != nil {
		t.Fatal("a title-only template must keep the default layout")
	}
}

func TestRender_EmptyTitleKeepsTheDefault(t *testing.T) {
	msg, data := sampleForTest()
	out, err := mustParse(t, model.ChannelTypeWeCom, "", `{{if eq .Severity "info"}}FYI{{end}}`, "").Render(msg, data)
	if err != nil || out.Subject != msg.Subject {
		t.Fatalf("subject = %q, err = %v; want the default subject", out.Subject, err)
	}
}

func TestRender_ContentSeesTheRenderedTitleAndFields(t *testing.T) {
	msg, data := sampleForTest()
	tmpl := mustParse(t, model.ChannelTypeWeCom, "markdown", "[生产] {{.Title}}",
		`{{.Title}}|{{.RuleKey}}|{{.Value}}/{{.Threshold}}|{{.DeviceName}}|{{.DeviceIP}}|{{.Labels.job}}|{{.Labels.missing}}|{{.EventCount}}|{{.IncidentURL}}`)

	out, err := tmpl.Render(msg, data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "[生产] " + msg.Subject + "|host_cpu_high|93.5/90|web-01|10.0.0.12|node||3|https://ongrid.example.com/alerts/incidents/1024"
	if out.Rendered == nil || out.Rendered.Content != want || out.Rendered.Format != notify.FormatMarkdown {
		t.Fatalf("rendered = %+v\nwant content %q", out.Rendered, want)
	}
}

func TestRender_Functions(t *testing.T) {
	msg, data := sampleForTest()
	content := `{{json .Labels.job}} {{default "n/a" .RunbookURL}} {{default "n/a" .Annotations.summary}} ` +
		`{{date "2006-01-02" .OccurredAt}} {{dateInZone "15:04 MST" .OccurredAt "Asia/Shanghai"}} ` +
		`{{lower .Severity}} {{truncate 3 "abcdef"}} {{replace "_" " " .RuleKey}} {{contains "cpu" .RuleKey}} {{trim "  x "}}`
	out, err := mustParse(t, model.ChannelTypeWeCom, "", "", content).Render(msg, data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := `"node" https://wiki.example.com/runbooks/host-cpu n/a ` +
		templateTestNow.In(time.Local).Format("2006-01-02") + ` 16:30 CST ` +
		`critical abc… host cpu high true x`
	if out.Rendered.Content != want {
		t.Fatalf("content = %q\nwant      %q", out.Rendered.Content, want)
	}
}

func TestRender_ExecutionErrorReturnsTheDefaultMessage(t *testing.T) {
	msg, data := sampleForTest()
	tmpl := mustParse(t, model.ChannelTypeWeCom, "", "custom {{.Title}}", `{{dateInZone "15:04" .OccurredAt "Mars/Olympus"}}`)

	out, err := tmpl.Render(msg, data)
	if err == nil {
		t.Fatal("an unknown time zone must fail the render")
	}
	if out.Subject != msg.Subject || out.Rendered != nil {
		t.Fatalf("got %+v, want the untouched default message", out)
	}
}

func TestRender_OutputIsBounded(t *testing.T) {
	msg, data := sampleForTest()
	data.Labels["blob"] = strings.Repeat("x", 1024)
	content := strings.Repeat("{{.Labels.blob}}", 80) // 80 KiB
	_, err := mustParse(t, model.ChannelTypeWeCom, "", "", content).Render(msg, data)
	if !errors.Is(err, errRenderTooLarge) {
		t.Fatalf("err = %v, want errRenderTooLarge", err)
	}
}

func TestRender_EmptyContentFallsBack(t *testing.T) {
	msg, data := sampleForTest()
	_, err := mustParse(t, model.ChannelTypeWeCom, "", "", `{{if eq .Severity "info"}}x{{end}}`).Render(msg, data)
	if !errors.Is(err, ErrTemplateRenderedEmpty) {
		t.Fatalf("err = %v, want ErrTemplateRenderedEmpty", err)
	}
}

func TestRender_WebhookBodyMustBeJSON(t *testing.T) {
	msg, data := sampleForTest()
	if _, err := mustParse(t, model.ChannelTypeWebhook, "", "", `{"title": {{.Title}}}`).Render(msg, data); err == nil {
		t.Fatal("an unquoted string must make the webhook body invalid")
	}
	out, err := mustParse(t, model.ChannelTypeWebhook, "", "", `{"title": {{json .Title}}, "id": {{.IncidentID}}}`).Render(msg, data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(out.Rendered.Content), &body); err != nil || body["title"] != msg.Subject || body["id"] != float64(1024) {
		t.Fatalf("body = %s (%v)", out.Rendered.Content, err)
	}
}

func TestParseChannelTemplate_MarkdownWithoutContentUsesTheDefault(t *testing.T) {
	msg, data := sampleForTest()
	out, err := mustParse(t, model.ChannelTypeWeCom, "markdown", "", "").Render(msg, data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"**" + msg.Subject + "**", "主机 CPU 使用率过高", "web-01（10.0.0.12）", "当前值：93.5",
		`<font color="warning">CRITICAL</font>`, "[查看告警详情](https://ongrid.example.com/alerts/incidents/1024)"} {
		if !strings.Contains(out.Rendered.Content, want) {
			t.Fatalf("default markdown lacks %q:\n%s", want, out.Rendered.Content)
		}
	}
}

// Every starter is offered in the UI as a ready-to-save template, so each one
// must parse and render against the sample alert (webhook: as valid JSON).
func TestStarterTemplates_RenderForEveryChannelType(t *testing.T) {
	types := []string{model.ChannelTypeWebhook, model.ChannelTypeSlack, model.ChannelTypeFeishu,
		model.ChannelTypeDingTalk, model.ChannelTypeWeCom, model.ChannelTypeTelegram}
	for _, channelType := range types {
		for _, format := range []string{"", "markdown"} {
			if format == "markdown" && !SupportsMarkdown(channelType) {
				continue
			}
			for _, locale := range []string{"zh", "en"} {
				name := channelType + "/" + format + "/" + locale
				data := SampleTemplateData(locale, "", templateTestNow)
				src := StarterTemplate(channelType, format, locale)
				tmpl, err := ParseChannelTemplate(channelType, format, "", src)
				if err != nil {
					t.Fatalf("%s: parse: %v", name, err)
				}
				out, err := tmpl.Render(SampleMessage(data), data)
				if err != nil {
					t.Fatalf("%s: render: %v", name, err)
				}
				if strings.Contains(out.Rendered.Content, "<no value>") || strings.Contains(out.Rendered.Content, "{{") {
					t.Fatalf("%s: unrendered output:\n%s", name, out.Rendered.Content)
				}
				// Without a public URL no dangling link may be left behind.
				if strings.Contains(out.Rendered.Content, "]()") || strings.Contains(out.Rendered.Content, "<|") {
					t.Fatalf("%s: empty link:\n%s", name, out.Rendered.Content)
				}
			}
		}
	}
}

func TestTemplateDataFor_MergesIncidentAndMessage(t *testing.T) {
	value := 0.004
	incident := &model.Incident{
		ID: 7, Rule: "disk_full", Value: &value, EventCount: 2,
		FirstFiredAt:    templateTestNow.Add(-time.Hour),
		LabelsJSON:      `{"mountpoint":"/data","device_id":"9"}`,
		AnnotationsJSON: `{"summary":"disk almost full"}`,
	}
	msg := notify.Message{Subject: "disk_full", Severity: notify.SeverityWarning, OccurredAt: templateTestNow,
		Labels: map[string]string{"device_id": "9", "device_hostname": "db-01"}}

	data := templateDataFor(msg, incident, "")
	if data.RuleName != "disk_full" || data.Value != "0.004" || data.IncidentURL != "" ||
		data.Labels["mountpoint"] != "/data" || data.DeviceName != "db-01" || data.DeviceID != "9" ||
		data.Annotations["summary"] != "disk almost full" || !data.FiredAt.Equal(incident.FirstFiredAt) {
		t.Fatalf("data = %+v", data)
	}
}

func TestFormatTemplateNumber(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{90, "90"}, {93.456, "93.46"}, {100.001, "100"}, {0.5, "0.5"}, {-12.3, "-12.3"}, {0.00123, "0.00123"}, {1e20, "100000000000000000000"},
	} {
		v := tc.in
		if got := formatTemplateNumber(&v); got != tc.want {
			t.Errorf("formatTemplateNumber(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if formatTemplateNumber(nil) != "" {
		t.Error("a missing value must render empty")
	}
}

// MaybeNotify renders the channel template with the current incident row and
// sends the result through that channel only.
func TestMaybeNotify_AppliesTheChannelTemplate(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nil)
	uc.SetPublicURL("https://ongrid.example.com")
	value := 97.0
	incident := &model.Incident{Rule: "cpu_high", RuleName: "CPU High", Severity: "critical",
		Status: model.IncidentStatusOpen, DedupeKey: "host:1:cpu_high", Value: &value,
		FirstFiredAt: templateTestNow, LastFiredAt: templateTestNow}
	if err := repo.CreateIncident(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	repo.channels["ops-wecom"] = &model.Channel{ID: 3, Name: "ops-wecom", ChannelType: model.ChannelTypeWeCom, Enabled: true,
		ConfigJSON: `{"endpoint":"https://qyapi.example.test/send?key=k"}`, MessageFormat: "markdown",
		TitleTemplate: strPtr("【{{upper .Severity}}】{{.RuleName}}")}
	repo.channels["ops-slack"] = &model.Channel{ID: 4, Name: "ops-slack", ChannelType: model.ChannelTypeSlack, Enabled: true,
		ConfigJSON: `{"endpoint":"https://hooks.example.test/slack"}`}
	notifier := &fakeNotifier{}

	uc.MaybeNotify(context.Background(), &FiringResult{Incident: incident},
		notify.Message{Subject: "cpu_high: value=97", Severity: notify.SeverityCritical, OccurredAt: templateTestNow},
		NotifyOpts{Notifier: notifier, DefaultChannels: []string{"ops-wecom", "ops-slack"}})

	if len(notifier.msgs) != 2 {
		t.Fatalf("sent %d messages, want 2", len(notifier.msgs))
	}
	wecom, slack := notifier.msgs[0], notifier.msgs[1]
	if wecom.Subject != "【CRITICAL】CPU High" || wecom.Rendered == nil || wecom.Rendered.Format != notify.FormatMarkdown {
		t.Fatalf("wecom message = %+v", wecom)
	}
	for _, want := range []string{"**【CRITICAL】CPU High**", "当前值：97", "https://ongrid.example.com/alerts/incidents/"} {
		if !strings.Contains(wecom.Rendered.Content, want) {
			t.Fatalf("wecom content lacks %q:\n%s", want, wecom.Rendered.Content)
		}
	}
	if slack.Subject != "cpu_high: value=97" || slack.Rendered != nil {
		t.Fatalf("a channel without a template must get the default message, got %+v", slack)
	}
}

func TestMaybeNotify_BrokenTemplateSendsTheDefaultAndSaysSo(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nil)
	incident := &model.Incident{Rule: "cpu_high", Severity: "warning", Status: model.IncidentStatusOpen,
		DedupeKey: "host:1:cpu_high", FirstFiredAt: templateTestNow, LastFiredAt: templateTestNow}
	if err := repo.CreateIncident(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	repo.channels["ops"] = &model.Channel{ID: 3, Name: "ops", ChannelType: model.ChannelTypeDingTalk, Enabled: true,
		ConfigJSON:      `{"endpoint":"https://oapi.example.test/robot/send"}`,
		ContentTemplate: strPtr(`{{dateInZone "15:04" .OccurredAt "Mars/Olympus"}}`)}
	notifier := &fakeNotifier{}

	uc.MaybeNotify(context.Background(), &FiringResult{Incident: incident},
		notify.Message{Subject: "cpu_high", Severity: notify.SeverityWarning, OccurredAt: templateTestNow},
		NotifyOpts{Notifier: notifier, DefaultChannels: []string{"ops"}})

	if len(notifier.msgs) != 1 || notifier.msgs[0].Rendered != nil || notifier.msgs[0].Subject != "cpu_high" {
		t.Fatalf("messages = %+v, want one default message", notifier.msgs)
	}
	var reason string
	for _, ev := range repo.events {
		if ev.EventType == model.EventTypeNotificationSent {
			reason = ev.Reason
		}
	}
	if !strings.Contains(reason, "template failed") || !strings.Contains(reason, "Mars/Olympus") {
		t.Fatalf("notification_sent reason = %q, want the template failure", reason)
	}
}

func TestRetryWorker_SendsThroughTheChannelRowWithItsTemplate(t *testing.T) {
	repo := newFakeRepo()
	now := templateTestNow
	device := uint64(42)
	incident := &model.Incident{DeviceID: &device, Rule: "cpu_high", RuleName: "CPU High", Severity: "critical",
		Status: model.IncidentStatusOpen, DedupeKey: "host:42:cpu_high", Summary: "cpu_high on device_id=42",
		FirstFiredAt: now.Add(-10 * time.Minute), LastFiredAt: now.Add(-time.Minute)}
	if err := repo.CreateIncident(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	// Created in the UI: unknown to the env-config router, so a retry by
	// channel name could never reach it.
	repo.channels["ui-wecom"] = &model.Channel{ID: 9, Name: "ui-wecom", ChannelType: model.ChannelTypeWeCom, Enabled: true,
		ConfigJSON:    `{"endpoint":"https://qyapi.example.test/send?key=k"}`,
		TitleTemplate: strPtr("[retry] {{.RuleName}} on {{.DeviceName}}")}
	finished := now.Add(-5 * time.Minute)
	repo.deliveries = append(repo.deliveries, &model.Delivery{ID: 1, IncidentID: &incident.ID, ChannelID: 9,
		Status: model.DeliveryStatusFailed, AttemptCount: 1, SentAt: &finished, FinishedAt: &finished})
	notifier := &fakeNotifier{}

	NewRetryWorker(RetryWorkerOpts{
		Repo: repo, Notifier: notifier, Usecase: NewUsecase(repo, nil), BackoffPerAttempt: time.Minute,
		Now: func() time.Time { return now },
		DeviceIdentityResolver: func(context.Context, uint64) (DeviceIdentity, error) {
			return DeviceIdentity{Hostname: "web-01", IPAddress: "10.0.0.12"}, nil
		},
	}).RunOnce(context.Background())

	if len(notifier.msgs) != 1 || len(notifier.channels) != 1 || notifier.channels[0][0] != "ui-wecom" {
		t.Fatalf("sent %d messages via %v, want one via ui-wecom", len(notifier.msgs), notifier.channels)
	}
	if got := notifier.msgs[0].Subject; got != "[retry] CPU High on web-01" {
		t.Fatalf("subject = %q", got)
	}
	if repo.deliveries[0].Status != model.DeliveryStatusSuccess {
		t.Fatalf("delivery status = %q, want success", repo.deliveries[0].Status)
	}
}
