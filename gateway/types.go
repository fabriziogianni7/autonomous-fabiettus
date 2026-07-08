package gateway

import "context"

// IncomingMessage is the normalized format for messages from any platform.
type IncomingMessage struct {
	Platform     string // "telegram"
	UserID       string // platform-specific user identifier
	ChatID       string // where to send the reply (channel, chat, etc.)
	Text         string
	ReplyToID    string // optional, for threading
	IsRestricted bool   // true when the message originates from a restricted context (e.g. a group chat); agent should block dangerous tools
}

// Handler processes an incoming message and returns the reply.
type Handler func(msg IncomingMessage) string

// Gateway receives messages from a platform and sends replies.
// Run blocks until the context is cancelled.
type Gateway interface {
	Run(ctx context.Context, handler Handler) error
}
