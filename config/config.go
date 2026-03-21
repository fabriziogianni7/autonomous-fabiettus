package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

func parseInt(s string, defaultVal int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return n
}

func parseInt64(s string, defaultVal int64) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultVal
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return defaultVal
	}
	return n
}

func parseBool(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "1" || s == "true" || s == "yes"
}

// Config holds validated application configuration.
type Config struct {
	TelegramBotToken    string
	GroqAPIKey          string
	BraveSearchAPIKey   string
	CompactionThreshold int    // optional, token count to trigger compaction (default 4000)
	OllamaURL           string // optional, e.g. "http://localhost:11434" for embeddings
	OllamaEmbedModel    string // optional, e.g. "nomic-embed-text" (default)

	// Wallet (optional). If EVM_RPC_URL and signer are set, wallet tools are enabled.
	EVM_RPC_URL            string // e.g. "https://eth-mainnet.g.alchemy.com/v2/..."
	ChainID                int64  // e.g. 1 for mainnet (used when WALLET_CHAINS not set)
	WalletSignerBackend    string // "env" | "kms" | "hsm" (default: env)
	WalletPrivateKeyEnv    string // env var name for key (default: WALLET_PRIVATE_KEY)
	WalletAccountMode      string // "eoa" | "smart" (default: eoa)
	WalletNativeSpendLimit string // wei string for auto-allow threshold, "0" = require approval for all
	WalletApprovalDir      string // dir for approval persistence (optional)

	// Multichain: JSON array of {chain_id, rpc_url, explorer, name}. If empty, use EVM_RPC_URL+CHAIN_ID.
	WalletChainsJSON     string // e.g. [{"chain_id":1,"rpc_url":"...","explorer":"https://etherscan.io","name":"Ethereum"}]
	WalletDefaultChainID int64  // default chain when chain_id omitted (default: from first chain or CHAIN_ID)

	// Skills: directory for user-installed skills (OpenClaw-style SKILL.md folders). Default: ./skills-data
	SkillsDir string

	// DataRoot: base directory for all persistent data (sessions, memories, reminders, etc.).
	// Set via RAILWAY_VOLUME_MOUNT_PATH (Railway) or DATA_ROOT. Default: "."
	DataRoot string

	// Autonomous mode: use x402 router for LLM instead of Groq, require wallet for permits.
	AutonomousMode bool   // when true, use X402_ROUTER_URL for LLM, GROQ_API_KEY optional
	UseGroqLLM     bool   // when true in autonomous mode, use Groq for LLM instead of x402 (for testing)
	X402RouterURL  string // default https://ai.xgate.run/v1
	X402PermitCap  string // optional session spend cap in USDC (default "50")
	X402Model      string // model for x402 router (default openai:gpt-4); use "auto" for router auto-selection

	// Opportunity scan cron (autonomous mode only). 0 = disabled.
	OpportunityScanIntervalMinutes int    // default 0
	TelegramOwnerChatID            string // chat ID to receive scan output and approvals (routed via existing bot)
	TelegramAllowedUserID          string // when set, only this Telegram user ID can chat with the bot (owner-only)

	// Alchemy Data API (optional). When set, portfolio tools (wallet_get_portfolio, wallet_get_portfolio_value,
	// wallet_get_activity) are enabled. Uses Token API, Prices API, Transfers API.
	// ALCHEMY_API_KEY enables Alchemy; URLs are derived per chain. Or set ALCHEMY_BASE_URL for a single explicit URL.
	AlchemyAPIKey  string // e.g. from dashboard.alchemy.com; when set, portfolio tools enabled
	AlchemyBaseURL string // optional override; when set, use for default chain instead of deriving from key

	// X402MinBaseUSDC: minimum USDC to keep on Base (chain 8453) for inference costs. Agent must not trade below this.
	// Default 10 when autonomous mode enabled. Used for runway checks via wallet_get_portfolio_value.
	X402MinBaseUSDC string // e.g. "10"

	// X402LLMTimeout: HTTP timeout for LLM requests in seconds (default 120). Increase if ai.xgate.run often times out.
	X402LLMTimeout int

	// Role-specific subagent models (autonomous mode). When spawn_subagents uses role, maps to model.
	// Omit to use X402Model for all. Saves cost: parser/research use cheap models.
	X402ModelQuant    string // quant: math/strategy
	X402ModelParser   string // parser: extraction/parsing
	X402ModelResearch string // research: web search/info
	X402ModelRisk     string // risk: exposure/VaR
	X402ModelSubagent string // default when role omitted or unknown

	// Subagent timeouts (seconds). Quant often needs longer for EV/Kelly calculations.
	SubagentTimeoutSec      int // default 60
	SubagentQuantTimeoutSec int // default 90; used when role=quant

	// DisableSubagents: when true, spawn_subagents returns instructions to do the work inline (no subagent).
	// Set via DISABLE_SUBAGENTS=1 or true. Makes quant analysis sequential in the main agent.
	DisableSubagents bool

	// FailureAnalyzerEnabled: FAILURE_ANALYZER=1 runs one failure-analyzer subagent after non-terminal tool failures.
	FailureAnalyzerEnabled bool
	// X402ModelAnalyzer: optional model for role failure-analyzer (autonomous x402).
	X402ModelAnalyzer string
}

// Load reads environment variables from .env (if present) and validates required values.
// Returns an error if any required variable is missing or invalid.
func Load() (*Config, error) {
	_ = godotenv.Load() // ignore error if .env doesn't exist

	cfg := &Config{
		TelegramBotToken:    strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		GroqAPIKey:          strings.TrimSpace(os.Getenv("GROQ_API_KEY")),
		BraveSearchAPIKey:   strings.TrimSpace(os.Getenv("BRAVE_SEARCH_API_KEY")),
		CompactionThreshold: parseInt(os.Getenv("CONTEXT_COMPACTION_THRESHOLD"), 4000),
		OllamaURL:           strings.TrimSpace(os.Getenv("OLLAMA_URL")),
		OllamaEmbedModel:    strings.TrimSpace(os.Getenv("OLLAMA_EMBED_MODEL")),

		EVM_RPC_URL:                    strings.TrimSpace(os.Getenv("EVM_RPC_URL")),
		ChainID:                        parseInt64(os.Getenv("CHAIN_ID"), 1),
		WalletSignerBackend:            strings.TrimSpace(os.Getenv("WALLET_SIGNER_BACKEND")),
		WalletPrivateKeyEnv:            strings.TrimSpace(os.Getenv("WALLET_PRIVATE_KEY_ENV")),
		WalletAccountMode:              strings.TrimSpace(os.Getenv("WALLET_ACCOUNT_MODE")),
		WalletNativeSpendLimit:         strings.TrimSpace(os.Getenv("WALLET_NATIVE_SPEND_LIMIT")),
		WalletApprovalDir:              strings.TrimSpace(os.Getenv("WALLET_APPROVAL_DIR")),
		WalletChainsJSON:               strings.TrimSpace(os.Getenv("WALLET_CHAINS")),
		WalletDefaultChainID:           parseInt64(os.Getenv("WALLET_DEFAULT_CHAIN_ID"), 0),
		SkillsDir:                      strings.TrimSpace(os.Getenv("SKILLS_DIR")),
		AutonomousMode:                 parseBool(os.Getenv("AUTONOMOUS_MODE")),
		UseGroqLLM:                     parseBool(os.Getenv("USE_GROQ_LLM")),
		X402RouterURL:                  strings.TrimSpace(os.Getenv("X402_ROUTER_URL")),
		X402PermitCap:                  strings.TrimSpace(os.Getenv("X402_PERMIT_CAP")),
		X402Model:                      strings.TrimSpace(os.Getenv("X402_MODEL")),
		OpportunityScanIntervalMinutes: parseInt(os.Getenv("OPPORTUNITY_SCAN_INTERVAL_MINUTES"), 0),
		TelegramOwnerChatID:            strings.TrimSpace(os.Getenv("TELEGRAM_OWNER_CHAT_ID")),
		TelegramAllowedUserID:          strings.TrimSpace(os.Getenv("TELEGRAM_ALLOWED_USER_ID")),
		AlchemyAPIKey:                  strings.TrimSpace(os.Getenv("ALCHEMY_API_KEY")),
		AlchemyBaseURL:                 strings.TrimSpace(os.Getenv("ALCHEMY_BASE_URL")),
		X402MinBaseUSDC:                strings.TrimSpace(os.Getenv("X402_MIN_BASE_USDC")),
		X402LLMTimeout:                 parseInt(os.Getenv("X402_LLM_TIMEOUT"), 120),
		X402ModelQuant:                 strings.TrimSpace(os.Getenv("X402_MODEL_QUANT")),
		X402ModelParser:                strings.TrimSpace(os.Getenv("X402_MODEL_PARSER")),
		X402ModelResearch:              strings.TrimSpace(os.Getenv("X402_MODEL_RESEARCH")),
		X402ModelRisk:                  strings.TrimSpace(os.Getenv("X402_MODEL_RISK")),
		X402ModelSubagent:              strings.TrimSpace(os.Getenv("X402_MODEL_SUBAGENT")),
		SubagentTimeoutSec:             parseInt(os.Getenv("SUBAGENT_TIMEOUT_SEC"), 60),
		SubagentQuantTimeoutSec:        parseInt(os.Getenv("SUBAGENT_QUANT_TIMEOUT_SEC"), 90),
		DisableSubagents:               parseBool(os.Getenv("DISABLE_SUBAGENTS")),
		FailureAnalyzerEnabled:         parseBool(os.Getenv("FAILURE_ANALYZER")),
		X402ModelAnalyzer:              strings.TrimSpace(os.Getenv("X402_MODEL_ANALYZER")),
	}
	cfg.DataRoot = strings.TrimSpace(os.Getenv("RAILWAY_VOLUME_MOUNT_PATH"))
	if cfg.DataRoot == "" {
		cfg.DataRoot = strings.TrimSpace(os.Getenv("DATA_ROOT"))
	}
	if cfg.DataRoot == "" {
		cfg.DataRoot = "."
	}
	if cfg.SkillsDir == "" {
		cfg.SkillsDir = "./skills-data"
	}
	if cfg.X402RouterURL == "" {
		cfg.X402RouterURL = "https://ai.xgate.run/v1"
	}
	if cfg.X402PermitCap == "" {
		cfg.X402PermitCap = "50"
	}
	if cfg.X402Model == "" {
		cfg.X402Model = "openai:gpt-4"
	}
	if cfg.AutonomousMode && cfg.X402MinBaseUSDC == "" {
		cfg.X402MinBaseUSDC = "10"
	}
	if cfg.X402LLMTimeout <= 0 {
		cfg.X402LLMTimeout = 120
	}
	if cfg.SubagentTimeoutSec <= 0 {
		cfg.SubagentTimeoutSec = 60
	}
	if cfg.SubagentQuantTimeoutSec <= 0 {
		cfg.SubagentQuantTimeoutSec = 90
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	var missing []string

	if c.AutonomousMode {
		// Autonomous mode: require wallet for x402 permits, GROQ_API_KEY optional
		if c.EVM_RPC_URL == "" {
			missing = append(missing, "EVM_RPC_URL (required for autonomous mode)")
		}
		if c.WalletSignerBackend == "" {
			c.WalletSignerBackend = "env"
		}
		if c.WalletPrivateKeyEnv == "" {
			c.WalletPrivateKeyEnv = "WALLET_PRIVATE_KEY"
		}
		if c.WalletSignerBackend == "env" && os.Getenv(c.WalletPrivateKeyEnv) == "" {
			missing = append(missing, c.WalletPrivateKeyEnv+" (required for autonomous mode)")
		}
		if c.X402RouterURL == "" {
			missing = append(missing, "X402_ROUTER_URL or set default")
		}
		if c.OpportunityScanIntervalMinutes > 0 {
			if c.TelegramBotToken == "" {
				missing = append(missing, "TELEGRAM_BOT_TOKEN (required for opportunity scan; notifications routed through bot)")
			}
			if c.TelegramOwnerChatID == "" {
				missing = append(missing, "TELEGRAM_OWNER_CHAT_ID (chat to receive scan output and approvals)")
			}
		}
		// Autonomous mode: require Base (chain 8453) for x402 USDC payments (http_request, etc.)
		if !hasBaseChain(c) {
			missing = append(missing, "Base (chain 8453) in WALLET_CHAINS or CHAIN_ID=8453 (x402 requires USDC on Base)")
		}
		if c.UseGroqLLM && c.GroqAPIKey == "" {
			missing = append(missing, "GROQ_API_KEY (required when USE_GROQ_LLM=1)")
		}
		// Autonomous mode: require Alchemy for portfolio valuation and USDC runway checks
		if !c.AlchemyEnabled() {
			missing = append(missing, "ALCHEMY_API_KEY or Alchemy EVM_RPC_URL (required for autonomous mode USDC runway checks)")
		}
	} else {
		// Non-autonomous: require Groq API key
		if c.GroqAPIKey == "" {
			missing = append(missing, "GROQ_API_KEY")
		}
	}

	if c.BraveSearchAPIKey == "" {
		missing = append(missing, "BRAVE_SEARCH_API_KEY")
	}

	// Telegram is the only gateway
	if c.TelegramBotToken == "" {
		missing = append(missing, "TELEGRAM_BOT_TOKEN")
	}

	// Wallet: if EVM_RPC_URL set, require WALLET_PRIVATE_KEY (or backend-specific key)
	if c.EVM_RPC_URL != "" {
		if c.WalletSignerBackend == "" {
			c.WalletSignerBackend = "env"
		}
		if c.WalletPrivateKeyEnv == "" {
			c.WalletPrivateKeyEnv = "WALLET_PRIVATE_KEY"
		}
		if c.WalletAccountMode == "" {
			c.WalletAccountMode = "eoa"
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s (set them in .env or export)", strings.Join(missing, ", "))
	}

	return nil
}

// hasBaseChain returns true if Base (chain 8453) is configured for the wallet.
func hasBaseChain(c *Config) bool {
	const baseChainID = 8453
	if c.WalletChainsJSON != "" {
		var chains []struct {
			ChainID int64 `json:"chain_id"`
		}
		if err := json.Unmarshal([]byte(c.WalletChainsJSON), &chains); err != nil {
			return false
		}
		for _, ch := range chains {
			if ch.ChainID == baseChainID {
				return true
			}
		}
		return false
	}
	return c.ChainID == baseChainID
}

// ModelForRole returns the x402 model for the given spawn_subagents role.
// Role is matched case-insensitively: quant, parser, research, risk, failure-analyzer (or analyzer).
// Empty role or unknown role returns fallback (X402ModelSubagent if set, else X402Model).
func (c *Config) ModelForRole(role string) string {
	fallback := c.X402ModelSubagent
	if fallback == "" {
		fallback = c.X402Model
	}
	r := strings.ToLower(strings.TrimSpace(role))
	if r == "" {
		return fallback
	}
	switch r {
	case "quant":
		if c.X402ModelQuant != "" {
			return c.X402ModelQuant
		}
	case "parser":
		if c.X402ModelParser != "" {
			return c.X402ModelParser
		}
	case "research":
		if c.X402ModelResearch != "" {
			return c.X402ModelResearch
		}
	case "risk":
		if c.X402ModelRisk != "" {
			return c.X402ModelRisk
		}
	case "failure-analyzer", "analyzer":
		if c.X402ModelAnalyzer != "" {
			return c.X402ModelAnalyzer
		}
	}
	return fallback
}

// AlchemyEnabled returns true if Alchemy Data API is configured (portfolio tools available).
func (c *Config) AlchemyEnabled() bool {
	return c.AlchemyAPIKey != "" || c.AlchemyBaseURL != "" ||
		(c.EVM_RPC_URL != "" && strings.Contains(c.EVM_RPC_URL, "alchemy.com"))
}

// WalletEnabled returns true if wallet should be initialized (RPC URL and signer key set).
func (c *Config) WalletEnabled() bool {
	if c.EVM_RPC_URL == "" {
		return false
	}
	if c.WalletSignerBackend == "" {
		c.WalletSignerBackend = "env"
	}
	if c.WalletPrivateKeyEnv == "" {
		c.WalletPrivateKeyEnv = "WALLET_PRIVATE_KEY"
	}
	// For env backend, require the key to be set
	if c.WalletSignerBackend == "env" {
		return os.Getenv(c.WalletPrivateKeyEnv) != ""
	}
	return true
}
