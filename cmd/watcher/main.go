package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"custom-agent/gateway"
	"custom-agent/skills"
	"custom-agent/wallet"
	"custom-agent/watcher/activity"
	watcheragent "custom-agent/watcher/agent"
	"custom-agent/watcher/webhook"

	"github.com/joho/godotenv"
	"github.com/sashabaranov/go-openai"
)

func main() {
	_ = godotenv.Load()
	token := strings.TrimSpace(os.Getenv("TELEGRAM_GROUP_BOT_TOKEN"))
	groupChatID := strings.TrimSpace(os.Getenv("TELEGRAM_GROUP_CHAT_ID"))
	groqKey := strings.TrimSpace(os.Getenv("GROQ_API_KEY"))
	dataRoot := strings.TrimSpace(os.Getenv("DATA_ROOT"))
	if dataRoot == "" {
		dataRoot = "."
	}
	port := strings.TrimSpace(os.Getenv("WATCHER_HTTP_PORT"))
	if port == "" {
		port = "8080"
	}
	walletAddr := strings.TrimSpace(os.Getenv("WATCHER_WALLET_ADDRESS"))
	signingKey := strings.TrimSpace(os.Getenv("ALCHEMY_WEBHOOK_SIGNING_KEY"))
	skillsDir := strings.TrimSpace(os.Getenv("SKILLS_DIR"))
	if skillsDir == "" {
		skillsDir = "./skills-data"
	}

	if token == "" || groupChatID == "" || groqKey == "" {
		log.Fatal("[watcher] TELEGRAM_GROUP_BOT_TOKEN, TELEGRAM_GROUP_CHAT_ID, GROQ_API_KEY required")
	}

	activityStore := activity.NewStore(dataRoot)
	senderRegistry := gateway.NewSenderRegistry()
	tg := gateway.NewTelegram(token, "", groupChatID)
	senderRegistry.Register("telegram", tg)
	notifier := wallet.NewSenderNotifier(senderRegistry)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/alchemy", webhook.Handler(activityStore, notifier, groupChatID, walletAddr, signingKey))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.Error(w, `404 - Not found. Alchemy webhook URL: POST /webhooks/alchemy`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("watcher ok. Alchemy webhook: POST /webhooks/alchemy"))
	})

	systemPrompt := loadSystemPrompt(skillsDir)
	llmConfig := openai.DefaultConfig(groqKey)
	llmConfig.BaseURL = "https://api.groq.com/openai/v1"
	llm := openai.NewClientWithConfig(llmConfig)
	qaAgent := watcheragent.New(llm, systemPrompt, activityStore)

	handler := func(msg gateway.IncomingMessage) string {
		return qaAgent.Respond(context.Background(), msg.Text)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := tg.Run(ctx, handler); err != nil && err != context.Canceled {
			log.Printf("[watcher] gateway: %v", err)
		}
	}()

	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		log.Printf("[watcher] HTTP listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[watcher] http: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("[watcher] shutting down...")
	cancel()
	_ = srv.Shutdown(context.Background())
}

func loadSystemPrompt(skillsDir string) string {
	personality, err := os.ReadFile("PERSONALITY_WATCHER.md")
	if err != nil {
		personality, _ = os.ReadFile("PERSONALITY.md")
	}
	if len(personality) == 0 {
		personality = []byte("You answer questions about Fabietto, an autonomous trading agent. You have no trading tools. Use the provided recent activity to answer.")
	}

	strategy, err := os.ReadFile("STRATEGY.md")
	if err != nil {
		strategy = []byte("")
	}

	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(string(personality)))
	if len(strategy) > 0 {
		sb.WriteString("\n\n")
		sb.WriteString(strings.TrimSpace(string(strategy)))
	}

	if skillsDir != "" {
		mgr := skills.NewManager(skillsDir)
		summaries, err := mgr.List()
		if err == nil && len(summaries) > 0 {
			sb.WriteString("\n\nSkills available: ")
			for i, s := range summaries {
				if i > 0 {
					sb.WriteString("; ")
				}
				sb.WriteString(s.Name)
				sb.WriteString(": ")
				sb.WriteString(s.Description)
			}
		}
	}

	return sb.String()
}
