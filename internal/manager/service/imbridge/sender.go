package imbridge

import (
	"context"
	"fmt"

	managerbizimbridgefeishu "github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/feishu"
	managerbizimbridgeslack "github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/slack"
	managerbizimbridgetelegram "github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/telegram"
	managerimbridgemodel "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// IMMessageSender sends to an explicit group through an IM Bridge app.
// Direct group sends are available only for providers with a stable outbound
// group API; DingTalk's current integration can only reply to an inbound
// session webhook and therefore rejects arbitrary group sends. WeCom AI bots
// send through the bot's live long connection (WeCom field).
type IMMessageSender struct {
	Apps imAppGetter
	// WeCom pushes through the long connection the stream supervisor holds
	// for a WeCom bot. Nil rejects WeCom sends.
	WeCom weComPusher
}

type imAppGetter interface {
	GetApp(ctx context.Context, id uint64) (*managerimbridgemodel.ImApp, error)
}

// weComPusher is satisfied by *wecom.Hub.
type weComPusher interface {
	Push(ctx context.Context, imAppID uint64, chatID, markdown string) error
}

func (s IMMessageSender) SendIMGroupMessage(ctx context.Context, imAppID uint64, groupID, text string) error {
	app, err := s.Apps.GetApp(ctx, imAppID)
	if err != nil {
		return fmt.Errorf("get IM app %d: %w", imAppID, err)
	}
	if app == nil {
		return fmt.Errorf("IM app %d: not found", imAppID)
	}
	if !app.Enabled {
		return fmt.Errorf("IM app %q is disabled", app.Name)
	}
	switch app.Provider {
	case managerimbridgemodel.ProviderFeishu:
		_, err = managerbizimbridgefeishu.NewClient(app.AppID, app.AppSecret).SendText(ctx, groupID, "chat_id", text)
	case managerimbridgemodel.ProviderTelegram:
		_, err = managerbizimbridgetelegram.NewClient(app.AppSecret).SendMessage(ctx, groupID, text)
	case managerimbridgemodel.ProviderSlack:
		client, clientErr := managerbizimbridgeslack.NewClientFromSecret(app.AppSecret)
		if clientErr != nil {
			return fmt.Errorf("build Slack IM client: %w", clientErr)
		}
		_, err = client.PostMessage(ctx, groupID, text)
	case managerimbridgemodel.ProviderDingTalk:
		return fmt.Errorf("DingTalk does not support direct group sends yet; its current IM Bridge integration only replies to inbound session webhooks")
	case managerimbridgemodel.ProviderWeCom:
		if s.WeCom == nil {
			return fmt.Errorf("WeCom sends need the bot's live long connection, which is not wired in this process")
		}
		// WeCom only delivers to chats where a user has messaged the bot.
		err = s.WeCom.Push(ctx, app.ID, groupID, text)
	default:
		return fmt.Errorf("unsupported IM provider %q", app.Provider)
	}
	if err != nil {
		return fmt.Errorf("send through %s app %q: %w", app.Provider, app.Name, err)
	}
	return nil
}
