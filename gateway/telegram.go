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

// formatMarkdownTablesForTelegram converts markdown tables to <pre> blocks with padded
// columns so they render as aligned tables in Telegram (monospace).
func formatMarkdownTablesForTelegram(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		if !strings.Contains(line, "|") {
			result = append(result, line)
			i++
			continue
		}
		// Collect consecutive table rows
		var tableLines []string
		for i < len(lines) && strings.Contains(lines[i], "|") {
			tableLines = append(tableLines, lines[i])
			i++
		}
		if len(tableLines) == 0 {
			continue
		}
		// Parse rows and skip separator line (|---|---|)
		var rows [][]string
		for _, l := range tableLines {
			cells := strings.Split(l, "|")
			for j, c := range cells {
				cells[j] = strings.TrimSpace(c)
			}
			// Remove empty first/last from leading/trailing |
			if len(cells) > 0 && cells[0] == "" {
				cells = cells[1:]
			}
			if len(cells) > 0 && cells[len(cells)-1] == "" {
				cells = cells[:len(cells)-1]
			}
			// Skip separator row (---|---|...)
			if len(cells) > 0 {
				allDash := true
				for _, c := range cells {
					if !regexp.MustCompile(`^[\s\-:]+$`).MatchString(c) {
						allDash = false
						break
					}
				}
				if !allDash {
					rows = append(rows, cells)
				}
			}
		}
		if len(rows) == 0 {
			for _, l := range tableLines {
				result = append(result, l)
			}
			continue
		}
		// Compute column widths
		maxCols := 0
		for _, r := range rows {
			if len(r) > maxCols {
				maxCols = len(r)
			}
		}
		widths := make([]int, maxCols)
		for _, r := range rows {
			for j, c := range r {
				if j >= maxCols {
					break
				}
				w := len([]rune(c))
				if w > widths[j] {
					widths[j] = w
				}
			}
		}
		// Build padded table
		var preLines []string
		for _, r := range rows {
			var parts []string
			for j := 0; j < maxCols; j++ {
				cell := ""
				if j < len(r) {
					cell = r[j]
				}
				pad := widths[j] - len([]rune(cell))
				parts = append(parts, cell+strings.Repeat(" ", pad))
			}
			preLines = append(preLines, strings.Join(parts, "  "))
		}
		tableBody := html.EscapeString(strings.Join(preLines, "\n"))
		result = append(result, "<pre>"+tableBody+"</pre>")
	}
	return strings.Join(result, "\n")
}

// convertMarkdownBoldToHTML converts **bold** to <b>bold</b> for Telegram HTML parse mode.
// Formats markdown tables as <pre> blocks for aligned display. Escapes HTML outside pre blocks.
func convertMarkdownBoldToHTML(s string) string {
	s = formatMarkdownTablesForTelegram(s)
	// Split on <pre>...</pre> blocks to avoid escaping them
	preRegex := regexp.MustCompile(`(?s)(<pre>.*?</pre>)`)
	parts := preRegex.Split(s, -1)
	preMatches := preRegex.FindAllString(s, -1)
	var out strings.Builder
	for i, part := range parts {
		escaped := html.EscapeString(part)
		escaped = regexp.MustCompile(`\*\*(.+?)\*\*`).ReplaceAllString(escaped, "<b>$1</b>")
		out.WriteString(escaped)
		if i < len(preMatches) {
			out.WriteString(preMatches[i])
		}
	}
	return out.String()
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
