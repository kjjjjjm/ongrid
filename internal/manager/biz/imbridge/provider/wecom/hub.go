package wecom

import (
	"context"
	"fmt"
	"sync"
)

// Hub tracks the live connection of every running WeCom bot, so callers
// outside the stream client (the send_im_message tool) can push messages
// through it. WeCom AI bots have no REST send API, and opening a second
// connection would evict the supervisor's. Create one Hub at boot and share it
// between NewStreamFactory and the IM message sender. A nil *Hub is valid and
// tracks nothing.
type Hub struct {
	mu      sync.RWMutex
	clients map[uint64]*StreamClient // im_apps.id → connected client
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{clients: map[uint64]*StreamClient{}}
}

// Connected reports whether app imAppID has a live connection that
// authenticated with exactly this BotID and Secret.
func (h *Hub) Connected(imAppID uint64, botID, secret string) bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	c := h.clients[imAppID]
	h.mu.RUnlock()
	return c != nil && c.app.AppID == botID && c.app.AppSecret == secret && c.connection() != nil
}

func (h *Hub) attach(imAppID uint64, c *StreamClient) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.clients[imAppID] = c
	h.mu.Unlock()
}

// detach removes c unless a newer client for the same app replaced it; after
// a credential rotation the old client can disconnect after the new one has
// attached.
func (h *Hub) detach(imAppID uint64, c *StreamClient) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.clients[imAppID] == c {
		delete(h.clients, imAppID)
	}
	h.mu.Unlock()
}

// Push sends a Markdown message to chatID through the live connection of the
// WeCom app imAppID. chatID is a group chatid, or the user's userid for a
// direct chat. WeCom delivers only to chats where a user has already messaged
// the bot.
func (h *Hub) Push(ctx context.Context, imAppID uint64, chatID, markdown string) error {
	if h == nil {
		return errNotConnected
	}
	h.mu.RLock()
	c := h.clients[imAppID]
	h.mu.RUnlock()
	if c == nil {
		return fmt.Errorf("wecom app %d has no live long connection; check that it is enabled and its stream state is running", imAppID)
	}
	return c.push(ctx, chatID, sendChatTypeAuto, markdown)
}
