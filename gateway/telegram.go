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

// telegramMaxMessageLen is Telegram's limit; split longer messages into chunks.
const telegramMaxMessageLen = 4090

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

// splitForTelegram splits text into chunks of at most telegramMaxMessageLen runes.
func splitForTelegram(text string) []string {
	runes := []rune(text)
	if len(runes) <= telegramMaxMessageLen {
		return []string{text}
	}
	var out []string
	for i := 0; i < len(runes); i += telegramMaxMessageLen {
		end := i + telegramMaxMessageLen
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}

// convertMarkdownBoldToHTML converts **bold** to <b>bold</b> for Telegram HTML parse mode.
// Escapes HTML entities in content so Telegram renders correctly.
func convertMarkdownBoldToHTML(s string) string {
	escaped := html.EscapeString(s)
	return regexp.MustCompile(`\*\*(.+?)\*\*`).ReplaceAllString(escaped, "<b>$1</b>")
}

// Send delivers an outbound message to the chat. Implements Sender.
// Splits text into chunks if it exceeds Telegram's message length limit.
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
	chunks := splitForTelegram(text)
	for _, chunk := range chunks {
		msg := tgbotapi.NewMessage(cid, convertMarkdownBoldToHTML(chunk))
		msg.ParseMode = "HTML"
		if _, err := bot.Send(msg); err != nil {
			return err
		}
	}
	return nil
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

			chunks := splitForTelegram(reply)
			for i, chunk := range chunks {
				resp := tgbotapi.NewMessage(msg.Chat.ID, convertMarkdownBoldToHTML(chunk))
				resp.ParseMode = "HTML"
				if i == 0 {
					resp.ReplyToMessageID = msg.MessageID
				}
				if _, err := bot.Send(resp); err != nil {
					log.Printf("[telegram] send error: %v", err)
					break
				}
			}
		}
	}
}
