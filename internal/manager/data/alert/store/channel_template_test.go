package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/config"
)

func TestChannelTemplateColumnsRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	title := "【{{upper .Severity}}】{{.RuleName}}"
	ch := &model.Channel{Name: "ops", ChannelType: model.ChannelTypeWeCom, Enabled: true, ConfigJSON: `{"endpoint":"https://x"}`,
		MessageFormat: "markdown", TitleTemplate: &title}
	if err := repo.CreateChannel(ctx, ch); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	got, err := repo.GetChannelByID(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetChannelByID: %v", err)
	}
	if got.MessageFormat != "markdown" || got.TitleTemplateText() != title || got.ContentTemplate != nil {
		t.Fatalf("row = format %q title %q content %v", got.MessageFormat, got.TitleTemplateText(), got.ContentTemplate)
	}

	content := "{{.Title}}"
	got.ContentTemplate, got.TitleTemplate, got.MessageFormat = &content, nil, ""
	if err := repo.UpdateChannel(ctx, ch.ID, got); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	got, err = repo.GetChannelByID(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetChannelByID: %v", err)
	}
	if got.MessageFormat != "" || got.TitleTemplate != nil || got.ContentTemplateText() != content {
		t.Fatalf("after update: format %q title %v content %q", got.MessageFormat, got.TitleTemplate, got.ContentTemplateText())
	}
}

// Env seeding rewrites config_json on every boot; the template columns of an
// env-configured channel must survive it, and the webhook secret must be kept
// so senders built from the row can sign.
func TestSeedChannelsFromConfigKeepsTemplatesAndStoresTheSecret(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	cfg := config.NotificationConfig{}
	cfg.DingTalk.Name, cfg.DingTalk.Enabled = "env-dingtalk", true
	cfg.DingTalk.URL, cfg.DingTalk.Secret = "https://oapi.example.test/robot/send?access_token=t", "SEC123"

	if err := SeedChannelsFromConfig(ctx, repo, cfg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ch, err := repo.GetChannelByName(ctx, "env-dingtalk")
	if err != nil {
		t.Fatalf("GetChannelByName: %v", err)
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(ch.ConfigJSON), &stored); err != nil || stored["secret"] != "SEC123" {
		t.Fatalf("config_json = %s (%v), want the secret kept", ch.ConfigJSON, err)
	}

	tmpl := "### {{.Title}}"
	ch.ContentTemplate, ch.MessageFormat = &tmpl, "markdown"
	if err := repo.UpdateChannel(ctx, ch.ID, ch); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if err := SeedChannelsFromConfig(ctx, repo, cfg); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	ch, err = repo.GetChannelByName(ctx, "env-dingtalk")
	if err != nil {
		t.Fatalf("GetChannelByName: %v", err)
	}
	if ch.ContentTemplateText() != tmpl || ch.MessageFormat != "markdown" {
		t.Fatalf("re-seed wiped the template: format %q content %q", ch.MessageFormat, ch.ContentTemplateText())
	}
}

// Rows written before the template columns existed read back as "no
// template": NULL text columns and the '' format default.
func TestChannelTemplateColumnsAreNullableForExistingRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(`CREATE TABLE notification_channels (
		id integer PRIMARY KEY AUTOINCREMENT, name text NOT NULL DEFAULT '', channel_type text NOT NULL DEFAULT '',
		enabled numeric NOT NULL DEFAULT true, config_json text NOT NULL, match_severity_min text NOT NULL DEFAULT '',
		match_scope_types text NOT NULL DEFAULT '', created_by integer, created_at datetime, updated_at datetime, deleted_at datetime)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec(`INSERT INTO notification_channels (name, channel_type, config_json) VALUES ('legacy', 'wecom', '{}')`).Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ch, err := NewRepo(db).GetChannelByName(context.Background(), "legacy")
	if err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if ch.HasTemplate() {
		t.Fatalf("legacy row reads as templated: %+v", ch)
	}
}
