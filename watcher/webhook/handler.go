package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"custom-agent/watcher/activity"
)

// isValidSignature verifies X-Alchemy-Signature per Alchemy docs:
// HMAC SHA-256(signingKey, rawBody) == header value.
func isValidSignature(body []byte, signature, signingKey string) bool {
	if signingKey == "" {
		return true
	}
	h := hmac.New(sha256.New, []byte(signingKey))
	h.Write(body)
	digest := hex.EncodeToString(h.Sum(nil))
	return hmac.Equal([]byte(digest), []byte(signature))
}

// Notifier sends messages to the group. Implemented by wallet.SenderNotifier.
type Notifier interface {
	Notify(ctx context.Context, platform, userID, chatID, message string) error
}

// AlchemyEvent is the webhook V2 payload structure.
type AlchemyEvent struct {
	WebhookID string           `json:"webhookId"`
	ID        string           `json:"id"`
	CreatedAt string           `json:"createdAt"`
	Type      string           `json:"type"`
	Event     AlchemyEventData `json:"event"`
}

type AlchemyEventData struct {
	Network  string            `json:"network"`
	Activity []AlchemyActivity `json:"activity"`
}

type AlchemyActivity struct {
	BlockNum    string  `json:"blockNum"`
	Hash        string  `json:"hash"`
	FromAddress string  `json:"fromAddress"`
	ToAddress   string  `json:"toAddress"`
	Value       float64 `json:"value"`
	Asset       string  `json:"asset"`
	Category    string  `json:"category"`
}

var networkToExplorer = map[string]string{
	"ETH_MAINNET":   "https://etherscan.io/tx/",
	"BASE_MAINNET":  "https://basescan.org/tx/",
	"MATIC_MAINNET": "https://polygonscan.com/tx/",
	"ARB_MAINNET":   "https://arbiscan.io/tx/",
	"OPT_MAINNET":   "https://optimistic.etherscan.io/tx/",
}

func explorerURL(network, hash string) string {
	base, ok := networkToExplorer[network]
	if !ok {
		return "https://etherscan.io/tx/" + hash
	}
	return base + hash
}

// Handler returns an HTTP handler for Alchemy webhooks.
// signingKey: from Alchemy dashboard (webhook detail page). If empty, signature validation is skipped.
func Handler(store *activity.Store, notifier Notifier, groupChatID, walletAddress, signingKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("[webhook] read body: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		r.Body.Close()

		signature := r.Header.Get("x-alchemy-signature")
		if !isValidSignature(body, signature, signingKey) {
			log.Printf("[webhook] signature validation failed")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		var evt AlchemyEvent
		if err := json.Unmarshal(body, &evt); err != nil {
			log.Printf("[webhook] decode error: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if evt.Type != "ADDRESS_ACTIVITY" {
			w.WriteHeader(http.StatusOK)
			return
		}
		walletAddr := strings.ToLower(walletAddress)
		for _, a := range evt.Event.Activity {
			from := strings.ToLower(a.FromAddress)
			to := strings.ToLower(a.ToAddress)
			if walletAddr != "" && from != walletAddr && to != walletAddr {
				continue
			}
			e := &activity.Entry{
				Network:     evt.Event.Network,
				Hash:        a.Hash,
				FromAddress: a.FromAddress,
				ToAddress:   a.ToAddress,
				Asset:       a.Asset,
				Value:       a.Value,
				Category:    a.Category,
			}
			if err := store.Add(e); err != nil {
				log.Printf("[webhook] store add: %v", err)
			}
			if notifier != nil && groupChatID != "" {
				direction := "transfer"
				if walletAddr != "" {
					if from == walletAddr {
						direction = "sent"
					} else if to == walletAddr {
						direction = "received"
					}
				}
				summary := formatSummary(a.Asset, a.Value, direction, evt.Event.Network, a.Hash)
				if err := notifier.Notify(context.Background(), "telegram", "", groupChatID, summary); err != nil {
					log.Printf("[webhook] notify: %v", err)
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}

func formatSummary(asset string, value float64, direction, network, hash string) string {
	if asset == "" {
		asset = "token"
	}
	explorer := explorerURL(network, hash)
	netShort := strings.TrimSuffix(strings.TrimPrefix(network, "ETH_"), "_MAINNET")
	if netShort == "" {
		netShort = "mainnet"
	}
	subject := "Fabietto " + direction
	if direction == "transfer" {
		subject = "Transfer"
	}
	return subject + " " + formatValue(asset, value) + " on " + netShort + " — " + explorer
}

func formatValue(asset string, value float64) string {
	if value >= 1000 {
		return fmt.Sprintf("%.0f %s", value, asset)
	}
	if value >= 1 {
		return fmt.Sprintf("%.2f %s", value, asset)
	}
	return fmt.Sprintf("%.4f %s", value, asset)
}
