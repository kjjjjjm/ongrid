package alert

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func ptr(s string) *string { return &s }

func TestServiceCreateChannel_StoresAValidTemplate(t *testing.T) {
	repo := &channelRepoStub{}
	svc := &Service{repo: repo, log: slog.Default()}

	got, err := svc.CreateChannel(context.Background(), Caller{}, ChannelInput{
		Name: "ops-wecom", Type: model.ChannelTypeWeCom, Endpoint: "https://qyapi.example.test/send?key=k", Enabled: true,
		Format: ptr("Markdown"), TitleTemplate: ptr("【{{upper .Severity}}】{{.RuleName}}"), ContentTemplate: ptr(""),
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	row := repo.createdRow
	if row.MessageFormat != "markdown" || row.TitleTemplateText() != "【{{upper .Severity}}】{{.RuleName}}" || row.ContentTemplate != nil {
		t.Fatalf("row = format %q title %q content %v", row.MessageFormat, row.TitleTemplateText(), row.ContentTemplate)
	}
	if got.Format != "markdown" || got.TitleTemplate == "" || got.ContentTemplate != "" {
		t.Fatalf("dto = %+v", got)
	}
}

func TestServiceCreateChannel_RejectsBrokenTemplates(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   ChannelInput
	}{
		{"syntax", ChannelInput{Type: model.ChannelTypeWeCom, ContentTemplate: ptr("{{.Title")}},
		// Parses, but the field does not exist: caught by the sample render.
		{"unknown field", ChannelInput{Type: model.ChannelTypeWeCom, ContentTemplate: ptr("{{.Hostname}}")}},
		{"markdown on slack", ChannelInput{Type: model.ChannelTypeSlack, Format: ptr("markdown")}},
		{"webhook body not JSON", ChannelInput{Type: model.ChannelTypeWebhook, ContentTemplate: ptr(`{"title": {{.Title}}}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.Name, in.Endpoint = "c", "https://example.test/hook"
			_, err := (&Service{repo: &channelRepoStub{}, log: slog.Default()}).CreateChannel(context.Background(), Caller{}, in)
			if !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

// A template that renders nothing for the sample alert (it only targets
// other severities) is legitimate and must save.
func TestServiceCreateChannel_AcceptsATemplateThatIsEmptyForTheSample(t *testing.T) {
	svc := &Service{repo: &channelRepoStub{}, log: slog.Default()}
	_, err := svc.CreateChannel(context.Background(), Caller{}, ChannelInput{
		Name: "c", Type: model.ChannelTypeDingTalk, Endpoint: "https://example.test/hook",
		ContentTemplate: ptr(`{{if eq .Severity "info"}}FYI {{.Title}}{{end}}`),
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
}

const longEndpoint = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=0123456789abcdef0123456789abcdef"

func TestServiceUpdateChannel_KeepsTemplatesUnlessSent(t *testing.T) {
	repo := &channelRepoStub{rows: []*model.Channel{{
		ID: 5, Name: "ops", ChannelType: model.ChannelTypeWeCom, Enabled: true,
		ConfigJSON:    `{"endpoint":"` + longEndpoint + `"}`,
		MessageFormat: "markdown", TitleTemplate: ptr("custom {{.Title}}"),
	}}}
	svc := &Service{repo: repo, log: slog.Default()}

	// A client unaware of templates (no template fields) keeps them.
	if _, err := svc.UpdateChannel(context.Background(), Caller{}, 5, ChannelInput{Name: "ops-renamed", Endpoint: longEndpoint, Enabled: true}); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if row := repo.updatedRow; row.MessageFormat != "markdown" || row.TitleTemplateText() != "custom {{.Title}}" {
		t.Fatalf("templates lost: format %q title %q", row.MessageFormat, row.TitleTemplateText())
	}

	// Empty strings clear them.
	if _, err := svc.UpdateChannel(context.Background(), Caller{}, 5, ChannelInput{
		Name: "ops", Endpoint: longEndpoint, Enabled: true, Format: ptr(""), TitleTemplate: ptr(""),
	}); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if row := repo.updatedRow; row.HasTemplate() {
		t.Fatalf("templates not cleared: format %q title %v", row.MessageFormat, row.TitleTemplate)
	}
}

// The edit form shows the masked endpoint. Saving it back (to change only the
// template, say) must not replace the real URL with the truncated one.
func TestServiceUpdateChannel_MaskedEndpointKeepsTheStoredURL(t *testing.T) {
	repo := &channelRepoStub{rows: []*model.Channel{{
		ID: 5, Name: "ops", ChannelType: model.ChannelTypeWeCom, Enabled: true,
		ConfigJSON: `{"endpoint":"` + longEndpoint + `"}`,
	}}}
	svc := &Service{repo: repo, log: slog.Default()}
	masked := maskEndpoint(longEndpoint)
	if !strings.HasSuffix(masked, "...") {
		t.Fatalf("fixture endpoint is too short to be masked: %q", masked)
	}

	if _, err := svc.UpdateChannel(context.Background(), Caller{}, 5, ChannelInput{
		Name: "ops", Endpoint: masked, Enabled: true, ContentTemplate: ptr("{{.Title}}"),
	}); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	var cfg map[string]string
	if err := json.Unmarshal([]byte(repo.updatedRow.ConfigJSON), &cfg); err != nil || cfg["endpoint"] != longEndpoint {
		t.Fatalf("endpoint = %q (%v), want the stored URL", cfg["endpoint"], err)
	}

	const replacement = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=new"
	if _, err := svc.UpdateChannel(context.Background(), Caller{}, 5, ChannelInput{Name: "ops", Endpoint: replacement, Enabled: true}); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if err := json.Unmarshal([]byte(repo.updatedRow.ConfigJSON), &cfg); err != nil || cfg["endpoint"] != replacement {
		t.Fatalf("endpoint = %q, want the new URL", cfg["endpoint"])
	}
}

func TestServicePreviewChannelTemplate(t *testing.T) {
	svc := &Service{log: slog.Default()}
	ctx := context.Background()

	got, err := svc.PreviewChannelTemplate(ctx, Caller{Locale: "en"}, ChannelTemplateInput{
		Type: model.ChannelTypeWeCom, Format: "markdown", TitleTemplate: "[prod] {{.RuleName}}", ContentTemplate: "**{{.Title}}** {{.Value}}",
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if got.Title != "[prod] High host CPU usage" || got.Content != "**[prod] High host CPU usage** 93.5" || got.Format != "markdown" || got.DefaultLayout {
		t.Fatalf("preview = %+v", got)
	}

	// No content template + text: the bot's built-in layout.
	got, err = svc.PreviewChannelTemplate(ctx, Caller{}, ChannelTemplateInput{Type: model.ChannelTypeDingTalk})
	if err != nil || !got.DefaultLayout || !strings.HasPrefix(got.Content, "[CRITICAL] [测试]") {
		t.Fatalf("default preview = %+v, %v", got, err)
	}
	got, err = svc.PreviewChannelTemplate(ctx, Caller{}, ChannelTemplateInput{Type: model.ChannelTypeSlack})
	if err != nil || !got.DefaultLayout || got.Content != "" {
		t.Fatalf("slack default preview = %+v, %v", got, err)
	}

	for _, in := range []ChannelTemplateInput{
		{Type: "pager"},
		{Type: model.ChannelTypeWeCom, ContentTemplate: "{{.Title"},
		{Type: model.ChannelTypeWeCom, ContentTemplate: "{{.NoSuchField}}"},
	} {
		if _, err := svc.PreviewChannelTemplate(ctx, Caller{}, in); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("Preview(%+v) err = %v, want ErrInvalid", in, err)
		}
	}
}

func TestServiceChannelTemplateStarter(t *testing.T) {
	svc := &Service{log: slog.Default()}
	zh, err := svc.ChannelTemplateStarter(context.Background(), Caller{}, model.ChannelTypeWeCom, "markdown")
	if err != nil || !strings.Contains(zh.ContentTemplate, "级别") {
		t.Fatalf("zh starter = %+v, %v", zh, err)
	}
	en, err := svc.ChannelTemplateStarter(context.Background(), Caller{Locale: "en"}, model.ChannelTypeWeCom, "markdown")
	if err != nil || !strings.Contains(en.ContentTemplate, "Severity") {
		t.Fatalf("en starter = %+v, %v", en, err)
	}
	if _, err := svc.ChannelTemplateStarter(context.Background(), Caller{}, model.ChannelTypeTelegram, "markdown"); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("telegram markdown starter err = %v, want ErrInvalid", err)
	}
}

func TestServiceTestChannel_SendsTheSampleThroughTheTemplate(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()
	repo := &channelRepoStub{rows: []*model.Channel{{
		ID: 9, Name: "ops", ChannelType: model.ChannelTypeWeCom, Enabled: true,
		ConfigJSON: `{"endpoint":"` + srv.URL + `"}`, MessageFormat: "markdown",
	}}}
	svc := &Service{repo: repo, notifier: &fakeNotifier{}, log: slog.Default()}

	got, err := svc.TestChannel(context.Background(), Caller{}, 9)
	if err != nil || !got.Accepted {
		t.Fatalf("TestChannel = %+v, %v", got, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	markdown, _ := payload["markdown"].(map[string]any)
	content, _ := markdown["content"].(string)
	if payload["msgtype"] != "markdown" || !strings.Contains(content, "[测试]") || !strings.Contains(content, "web-01") {
		t.Fatalf("payload = %v", payload)
	}
}

func TestServiceTestChannel_ReportsTheBotRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":40058,"errmsg":"markdown.content exceed max length"}`)
	}))
	defer srv.Close()
	repo := &channelRepoStub{rows: []*model.Channel{{
		ID: 9, Name: "ops", ChannelType: model.ChannelTypeWeCom, Enabled: true, ConfigJSON: `{"endpoint":"` + srv.URL + `"}`,
	}}}
	svc := &Service{repo: repo, notifier: &fakeNotifier{}, log: slog.Default()}

	got, err := svc.TestChannel(context.Background(), Caller{}, 9)
	if err != nil || got.Accepted || !strings.Contains(got.Message, "40058") {
		t.Fatalf("TestChannel = %+v, %v; want the errcode reported", got, err)
	}
}
