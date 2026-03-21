package gateway

import (
	"context"
	"fmt"
	"html"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"custom-agent/wallet/redact"
)

// TelegramGateway implements Gateway and Sender for Telegram.
type TelegramGateway struct {
	token         string
	allowedUserID string // when set, only messages from this user are processed
	groupChatID   string // when set, only messages from this chat (group) are processed
	bot           *tgbotapi.BotAPI
	mu            sync.RWMutex
}

// NewTelegram creates a Telegram gateway. Token must be non-empty.
// allowedUserID: when non-empty, only messages from this Telegram user ID are processed (owner-only mode).
// groupChatID: when non-empty, only messages from this chat ID are processed (group-only mode). Pass "" for Fabiettus.
func NewTelegram(token string, allowedUserID string, groupChatID string) *TelegramGateway {
	return &TelegramGateway{
		token:         token,
		allowedUserID: strings.TrimSpace(allowedUserID),
		groupChatID:   strings.TrimSpace(groupChatID),
	}
}

// convertMarkdownBoldToHTML converts **bold** to <b>bold</b> for Telegram HTML parse mode.
// Escapes HTML entities in content so Telegram renders correctly.
func convertMarkdownBoldToHTML(s string) string {
	escaped := html.EscapeString(s)
	return regexp.MustCompile(`\*\*(.+?)\*\*`).ReplaceAllString(escaped, "<b>$1</b>")
}

// Send delivers an outbound message to the chat. Implements Sender.
func (g *TelegramGateway) Send(ctx context.Context, platform, userID, chatID, text string) error {
	g.mu.RLock()
	bot := g.bot
	g.mu.RUnlock()
	if bot == nil {
		return fmt.Errorf("telegram: bot not initialized")
	}
	cid, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: invalid chat_id %q: %w", chatID, err)
	}
	msg := tgbotapi.NewMessage(cid, convertMarkdownBoldToHTML(text))
	msg.ParseMode = "HTML"
	_, err = bot.Send(msg)
	return err
}

// Run starts the Telegram bot and processes messages until ctx is cancelled.
func (g *TelegramGateway) Run(ctx context.Context, handler Handler) error {
	bot, err := tgbotapi.NewBotAPI(g.token)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}

	g.mu.Lock()
	g.bot = bot
	g.mu.Unlock()

	bot.Debug = false
	log.Printf("[telegram] Authorized as @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update := <-updates:
			if update.Message == nil || update.Message.From == nil {
				continue
			}

			msg := update.Message
			chatIDStr := fmt.Sprintf("%d", msg.Chat.ID)
			userIDStr := fmt.Sprintf("%d", msg.From.ID)
			log.Printf("[telegram] message from chat=%s user=%s (@%s): %s", chatIDStr, userIDStr, msg.From.UserName, redact.Redact(msg.Text))
			if g.groupChatID != "" && chatIDStr != g.groupChatID {
				continue // group-only mode: ignore messages from other chats
			}
			if g.allowedUserID != "" && userIDStr != g.allowedUserID {
				log.Printf("[telegram] ignored message from user %s (not allowed)", userIDStr)
				continue
			}

			incoming := IncomingMessage{
				Platform:  "telegram",
				UserID:    userIDStr,
				ChatID:    chatIDStr,
				Text:      msg.Text,
				ReplyToID: fmt.Sprintf("%d", msg.MessageID),
			}

			reply := handler(incoming)

			response := tgbotapi.NewMessage(msg.Chat.ID, convertMarkdownBoldToHTML(reply))
			response.ParseMode = "HTML"
			response.ReplyToMessageID = msg.MessageID
			if _, err := bot.Send(response); err != nil {
				log.Printf("[telegram] send error: %v", err)
			}
		}
	}
}
