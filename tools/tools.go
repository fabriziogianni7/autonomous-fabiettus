package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"custom-agent/alchemy"
	"custom-agent/memory"
	"custom-agent/reminders"
	"custom-agent/skills"
	"custom-agent/spend"
	"custom-agent/wallet/redact"
	"custom-agent/x402client"

	openai "github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

// Tool names for fallback parsing
var toolNames = []string{"run_command", "read_file", "write_file", "web_search", "save_memory", "read_memory", "create_scheduled_reminder", "list_reminders", "delete_reminder", "spawn_subagents", "http_request", "x402_get_stats", "lifi_get_quote", "lifi_track_status", "lifi_check_route", "lifi_get_token", "wallet_get_balance", "wallet_execute_transfer", "wallet_execute_contract_call", "wallet_list_transactions", "wallet_get_portfolio", "wallet_get_portfolio_value", "wallet_get_activity", "list_skills", "read_skill", "read_skill_script", "write_skill"}

// ReadOnlyToolNames are tools allowed for stateless sub-agents (no session/memory/reminder writes).
var ReadOnlyToolNames = map[string]bool{
	"read_file":                  true,
	"web_search":                 true,
	"read_memory":                true,
	"http_request":               true,
	"x402_get_stats":             true,
	"lifi_get_quote":             true,
	"lifi_track_status":          true,
	"lifi_check_route":           true,
	"lifi_get_token":             true,
	"wallet_get_portfolio":       true,
	"wallet_get_portfolio_value": true,
	"wallet_get_activity":        true,
	"list_skills":                true,
	"read_skill":                 true,
	"read_skill_script":          true,
}

// WalletService is the interface for policy-gated wallet operations. Optional.
// Implemented by *wallet.Service when wallet is enabled.
// chainID 0 = default chain.
type WalletService interface {
	WalletAddress() string
	DefaultChainID() int64
	ChainIDs() []int64
	GetBalanceString(ctx context.Context, chainID int64, block interface{}) (string, error)
	ExecuteTransfer(ctx context.Context, chainID int64, to, valueWei, platform, userID, chatID string) (string, error)
	ExecuteContractCall(ctx context.Context, chainID int64, to, dataHex, valueWei, platform, userID, chatID string) (string, error)
	ExecuteApproved(ctx context.Context, approvalID, platform, userID, chatID string) (string, error)
	ListTransactions(chainID int64, limit int) (string, error)
}

// SkillsManager is the interface for listing and reading skills. Optional.
type SkillsManager interface {
	List() ([]skills.SkillSummary, error)
	Get(name string) (skills.Skill, error)
	ReadScript(skillName, relPath string) (string, error)
	Dir() string
}

// SkillsWriter extends SkillsManager with write capability for newSkill flow.
type SkillsWriter interface {
	SkillsManager
	WriteSkill(name string, skillMd string, scripts map[string]string) error
}

// Tools holds tool execution state (e.g. API keys). Create with NewTools.
type Tools struct {
	BraveSearchAPIKey string
	MemoryStore       *memory.Store
	ReminderStore     *reminders.Store
	Wallet            WalletService      // optional; when set, wallet tools are available
	X402Client        *x402client.Client // optional; when set, http_request can pay for 402-protected APIs
	X402RouterURL     string             // optional; when set with X402Client, x402_get_stats is available
	X402PermitCap     string             // optional; session spend cap in USDC for x402_get_stats remaining calc
	SpendStore        *spend.Store       // optional; when set (autonomous mode), x402_get_stats returns locally tracked stats
	Alchemy           *alchemy.Client    // optional; when set, portfolio tools are available
	Skills            SkillsManager      // optional; when set, skills tools are available
	LLMClient         *openai.Client     // optional; when set, write_skill runs security/feasibility checks
}

// NewTools creates a Tools instance with the given Brave Search API key and optional memory store.
func NewTools(braveSearchAPIKey string, memoryStore *memory.Store) *Tools {
	return NewToolsWithReminderStore(braveSearchAPIKey, memoryStore, reminders.NewStore())
}

// NewToolsWithReminderStore creates a Tools instance with the given reminder store (for sharing with cron).
func NewToolsWithReminderStore(braveSearchAPIKey string, memoryStore *memory.Store, reminderStore *reminders.Store) *Tools {
	return &Tools{
		BraveSearchAPIKey: braveSearchAPIKey,
		MemoryStore:       memoryStore,
		ReminderStore:     reminderStore,
	}
}

// SetWallet sets the optional wallet service. Call after construction when wallet is enabled.
func (t *Tools) SetWallet(w WalletService) {
	t.Wallet = w
}

// SetX402Client sets the optional x402-aware HTTP client. Call when wallet is enabled for paid API support.
func (t *Tools) SetX402Client(c *x402client.Client) {
	t.X402Client = c
}

// SetX402StatsConfig sets router URL and permit cap for x402_get_stats. Call when autonomous mode uses x402 router.
func (t *Tools) SetX402StatsConfig(routerURL, permitCap string) {
	t.X402RouterURL = strings.TrimSuffix(routerURL, "/")
	t.X402PermitCap = permitCap
}

// SetSpendStore sets the optional spend store for local inference tracking. When set, x402_get_stats returns locally tracked stats.
func (t *Tools) SetSpendStore(s *spend.Store) {
	t.SpendStore = s
}

// SetAlchemy sets the optional Alchemy client for portfolio tools.
func (t *Tools) SetAlchemy(c *alchemy.Client) {
	t.Alchemy = c
}

// SetSkills sets the optional skills manager. Call when skills are enabled.
func (t *Tools) SetSkills(sm SkillsManager) {
	t.Skills = sm
}

// SetLLMClient sets the optional LLM client for write_skill security/feasibility checks.
func (t *Tools) SetLLMClient(c *openai.Client) {
	t.LLMClient = c
}

// ParseToolCallFromContent extracts a tool call from model output when the model
// returns tool format as text. Supports:
//   - <write_file>{"path":"x","content":"y"}</function>
//   - <function=web_search>{"query":"..."}</function>
//   - (function=web_search>{"query":"..."}</function>  (model typo: ( instead of <)
//
// Returns name, args, and true if found.
func ParseToolCallFromContent(content string) (name string, args string, ok bool) {
	patterns := []string{
		`<` + `%s` + `>\s*(\{)`,           // <toolname>{
		`<function=` + `%s` + `>\s*(\{)`,  // <function=toolname>{
		`\(function=` + `%s` + `>\s*(\{)`, // (function=toolname>{ (typo)
	}
	for _, n := range toolNames {
		for _, pat := range patterns {
			re := regexp.MustCompile(fmt.Sprintf(pat, regexp.QuoteMeta(n)))
			loc := re.FindStringIndex(content)
			if loc == nil {
				continue
			}
			jsonStart := loc[1] - 1 // position of {
			argsStr, _ := extractJSON(content, jsonStart)
			if argsStr != "" {
				return n, argsStr, true
			}
		}
	}
	return "", "", false
}

// extractJSON extracts a complete JSON object starting at start, handling nested braces and strings.
func extractJSON(s string, start int) (string, int) {
	if start >= len(s) || s[start] != '{' {
		return "", -1
	}
	depth := 1
	inString := false
	quote := byte(0)
	escape := false
	for i := start + 1; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inString {
			escape = true
			continue
		}
		if (c == '"' || c == '\'') && !escape {
			if !inString {
				inString = true
				quote = c
			} else if c == quote {
				inString = false
			}
			continue
		}
		if inString {
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return s[start : i+1], i + 1
			}
		}
	}
	return "", -1
}

// Definitions returns the tool definitions for the LLM.
func Definitions() []openai.Tool {
	return []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "run_command",
				Description: "Run a shell command on the user's computer",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"command": {Type: jsonschema.String, Description: "The command to run"},
					},
					Required: []string{"command"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "read_file",
				Description: "Read a file from the filesystem",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"path": {Type: jsonschema.String, Description: "Path to the file"},
					},
					Required: []string{"path"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "write_file",
				Description: "Write content to a file",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"path":    {Type: jsonschema.String, Description: "Path to the file"},
						"content": {Type: jsonschema.String, Description: "Content to write"},
					},
					Required: []string{"path", "content"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "web_search",
				Description: "Search the web for information",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"query": {Type: jsonschema.String, Description: "Search query"},
					},
					Required: []string{"query"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "save_memory",
				Description: "Save a fact, preference, or important information to long-term memory. Use when the user shares something worth remembering (name, preferences, decisions). Survives session resets.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"content": {Type: jsonschema.String, Description: "The memory to store (concise, factual)"},
						"tags":    {Type: jsonschema.String, Description: "Optional comma-separated tags for organization"},
					},
					Required: []string{"content"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "read_memory",
				Description: "Search long-term memory for user preferences, past trade outcomes, and decisions. Use for context that does not change. Do NOT use for prices, portfolio, or market data—those require fresh fetches via http_request and wallet_* tools.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"query": {Type: jsonschema.String, Description: "What to search for (semantic search supported)"},
						"limit": {Type: jsonschema.Integer, Description: "Max memories to return (default 5)"},
					},
					Required: []string{"query"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "create_scheduled_reminder",
				Description: "Schedule a recurring reminder to send the user a message at a specific time. Use when the user asks to be reminded (e.g. 'remind me every day at 9am', 'check in with me every Monday'). Schedule format: cron expression (min hour day month weekday). Examples: '0 9 * * *' = daily 9am, '0 10 * * 1' = Mondays 10am, '30 8 * * 1-5' = weekdays 8:30am.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"schedule": {Type: jsonschema.String, Description: "Cron expression. 5-field: min hour day month weekday (e.g. 0 9 * * * daily 9am, */10 * * * * every 10 min). 6-field: sec min hour day month weekday (e.g. */10 * * * * * every 10 sec)."},
						"message":  {Type: jsonschema.String, Description: "The message to send when the reminder triggers"},
					},
					Required: []string{"schedule", "message"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_reminders",
				Description: "List the user's scheduled reminders. Use when the user asks what reminders they have or to show their schedule.",
				Parameters: jsonschema.Definition{
					Type:       jsonschema.Object,
					Properties: map[string]jsonschema.Definition{},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "delete_reminder",
				Description: "Delete a scheduled reminder by ID. Use when the user wants to cancel or remove a reminder. Get the ID from list_reminders.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"id": {Type: jsonschema.String, Description: "The reminder ID to delete"},
					},
					Required: []string{"id"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "spawn_subagents",
				Description: "Delegate independent subtasks to concurrent sub-agents. Use when a request can be parallelized (e.g. research multiple topics, compare several options, gather info from different angles). Each subtask runs in parallel. Sub-agents can use read_file, web_search, read_memory. Pass 2-5 focused tasks for best results.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"tasks": {Type: jsonschema.Array, Description: "List of independent subtasks to run in parallel", Items: &jsonschema.Definition{Type: jsonschema.String}},
						"role":  {Type: jsonschema.String, Description: "Optional role. Use 'quant' for math/strategy/position sizing; 'parser' for extraction/parsing; 'research' for web search/info gathering; 'risk' for exposure/VaR. Omit for general tasks. In autonomous mode, each role uses a cost-optimized model."},
					},
					Required: []string{"tasks"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "http_request",
				Description: "Make an HTTP request to a URL. Supports GET, POST, etc. When x402 is configured, automatically pays for 402-protected APIs. Use for fetching data from APIs, including paid x402 endpoints. Returns status, headers, and body (truncated if large).",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"url":     {Type: jsonschema.String, Description: "Full URL to request (e.g. https://api.example.com/data)"},
						"method":  {Type: jsonschema.String, Description: "HTTP method (default: GET). Use GET, POST, PUT, etc."},
						"headers": {Type: jsonschema.Object, Description: "Optional headers as key-value object (e.g. {\"Accept\": \"application/json\"})"},
						"body":    {Type: jsonschema.String, Description: "Optional request body for POST/PUT (omit for GET)"},
					},
					Required: []string{"url"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "x402_get_stats",
				Description: "Get x402 router session stats: total_spent_usd, total_tokens, permit_cap, remaining_usd. Use at start of opportunity scans and before capital deployment to check inference cost runway. Requires autonomous mode with x402 router.",
				Parameters: jsonschema.Definition{
					Type:       jsonschema.Object,
					Properties: map[string]jsonschema.Definition{},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "wallet_get_balance",
				Description: "Get the native token (ETH) balance of the wallet in wei. Use when the user asks about balance or funds. Omit chain_id for default chain.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"chain_id": {Type: jsonschema.Integer, Description: "Optional chain ID. Omit to use default chain."},
					},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "wallet_execute_transfer",
				Description: "REQUIRED to send ETH: call this tool when the user asks to send or transfer. You cannot send without invoking this tool. Params: to (0x...), value_wei (decimal string). Returns tx hash and explorer link. Requires approval if above policy limits.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"to":        {Type: jsonschema.String, Description: "Recipient address (0x...)"},
						"value_wei": {Type: jsonschema.String, Description: "Amount in wei as decimal string"},
						"chain_id":  {Type: jsonschema.Integer, Description: "Optional chain ID. Omit to use default chain."},
					},
					Required: []string{"to", "value_wei"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "wallet_execute_contract_call",
				Description: "REQUIRED to execute contract calls: call this tool when the user asks to call a contract or send to a contract. Params: to, data (hex), value_wei (0 or 0x0 for none), chain_id (required for LI.FI—use transactionRequest.chainId). Returns tx hash and explorer link.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"to":        {Type: jsonschema.String, Description: "Contract address (0x...)"},
						"data":      {Type: jsonschema.String, Description: "Hex-encoded calldata (0x...). Do not truncate."},
						"value_wei": {Type: jsonschema.String, Description: "ETH to send in wei. Decimal or hex (0x0 for none). LI.FI returns hex."},
						"chain_id":  {Type: jsonschema.Integer, Description: "Chain ID. Required for LI.FI—use transactionRequest.chainId. Omit for default chain."},
					},
					Required: []string{"to", "data"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "wallet_list_transactions",
				Description: "List recent agent-initiated wallet transactions with chain, status, hash, and explorer link. Use when the user asks about transaction history.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"chain_id": {Type: jsonschema.Integer, Description: "Optional chain ID to filter. Omit for all chains."},
						"limit":    {Type: jsonschema.Integer, Description: "Max transactions to return (default 20)."},
					},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "wallet_get_portfolio",
				Description: "Get full portfolio: native + ERC-20 token balances per chain. Requires Alchemy. Use when the user asks about holdings, positions, or what tokens they have.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"chain_id":     {Type: jsonschema.Integer, Description: "Optional chain ID. Omit for all configured chains."},
						"include_zero": {Type: jsonschema.Boolean, Description: "Include zero balances (default false)."},
					},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "wallet_get_portfolio_value",
				Description: "Get portfolio with USD valuation. Native + ERC-20 balances and total value. Use for runway, capital allocation, or PnL context.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"chain_id":     {Type: jsonschema.Integer, Description: "Optional chain ID. Omit for all configured chains."},
						"include_zero": {Type: jsonschema.Boolean, Description: "Include zero balances (default false)."},
					},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "wallet_get_activity",
				Description: "Get wallet activity: deposits, withdrawals, swap fills, incoming transfers. Use for PnL context or recent history beyond agent-initiated txs.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"chain_id": {Type: jsonschema.Integer, Description: "Optional chain ID. Omit for default chain."},
						"limit":    {Type: jsonschema.Integer, Description: "Max transfers to return (default 20)."},
					},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_skills",
				Description: "List the names and short descriptions of all available skills. Use when you need to see what skills exist before deciding which to use.",
				Parameters: jsonschema.Definition{
					Type:       jsonschema.Object,
					Properties: map[string]jsonschema.Definition{},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "read_skill",
				Description: "Read a skill by name. Use full=true to get full instructions and script paths; use full=false for description only.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"name": {Type: jsonschema.String, Description: "Skill name (from list_skills)"},
						"full": {Type: jsonschema.Boolean, Description: "If true, return full body and scripts. Default true."},
					},
					Required: []string{"name"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "read_skill_script",
				Description: "Read the contents of a script file within a skill (e.g. scripts/process.py). Use when the skill references scripts you need to run or inspect.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"name": {Type: jsonschema.String, Description: "Skill name"},
						"path": {Type: jsonschema.String, Description: "Relative path to script (e.g. scripts/process.py)"},
					},
					Required: []string{"name", "path"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "write_skill",
				Description: "Persist a new skill to disk. Automatically runs security and feasibility checks before saving. Use when the user asks to add, create, or install a skill—compose the SKILL.md (YAML frontmatter + body) and call this. Creates skills/<name>/SKILL.md and optional scripts.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"name":     {Type: jsonschema.String, Description: "Skill name (alphanumeric, hyphen, underscore only)"},
						"skill_md": {Type: jsonschema.String, Description: "Full SKILL.md content including YAML frontmatter"},
						"scripts":  {Type: jsonschema.Object, Description: "Optional map of relative path to content (e.g. {\"process.py\": \"...\"})"},
					},
					Required: []string{"name", "skill_md"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "lifi_get_quote",
				Description: "Get LI.FI swap or bridge quote. Returns optimal route, estimate (toAmount, fees, executionDuration), and transactionRequest ready for wallet_execute_contract_call. Use for any swap or cross-chain transfer. Omit from_address to use the configured wallet.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"from_chain":   {Type: jsonschema.Integer, Description: "Source chain ID (e.g. 8453 for Base)"},
						"to_chain":     {Type: jsonschema.Integer, Description: "Destination chain ID"},
						"from_token":   {Type: jsonschema.String, Description: "Source token address or 0x0000000000000000000000000000000000000000 for native"},
						"to_token":     {Type: jsonschema.String, Description: "Destination token address or 0x0... for native"},
						"from_amount":  {Type: jsonschema.String, Description: "Amount in token smallest unit (wei)"},
						"from_address": {Type: jsonschema.String, Description: "Wallet address. Omit to use configured wallet."},
						"to_address":   {Type: jsonschema.String, Description: "Optional recipient. Omit for same as from_address."},
						"slippage":     {Type: jsonschema.Number, Description: "Slippage tolerance 0-1 (e.g. 0.03 = 3%). Default 0.03."},
						"order":        {Type: jsonschema.String, Description: "RECOMMENDED, FASTEST, CHEAPEST, or SAFEST. Default RECOMMENDED."},
					},
					Required: []string{"from_chain", "to_chain", "from_token", "to_token", "from_amount"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "lifi_track_status",
				Description: "Track LI.FI cross-chain transfer status. Returns status (NOT_FOUND, PENDING, DONE, FAILED), substatus, and tx details.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"tx_hash":    {Type: jsonschema.String, Description: "Source chain transaction hash (0x...)"},
						"bridge":     {Type: jsonschema.String, Description: "Optional bridge key for faster lookup"},
						"from_chain": {Type: jsonschema.Integer, Description: "Optional source chain ID"},
						"to_chain":   {Type: jsonschema.Integer, Description: "Optional destination chain ID"},
					},
					Required: []string{"tx_hash"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "lifi_check_route",
				Description: "Check if a LI.FI route exists between chains and tokens. Use before quoting to verify the pair is supported.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"from_chain": {Type: jsonschema.Integer, Description: "Source chain ID"},
						"to_chain":   {Type: jsonschema.Integer, Description: "Destination chain ID"},
						"from_token": {Type: jsonschema.String, Description: "Source token address or 0x0... for native"},
						"to_token":   {Type: jsonschema.String, Description: "Optional destination token. Omit to check any destination."},
					},
					Required: []string{"from_chain", "to_chain", "from_token"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "lifi_get_token",
				Description: "Get LI.FI token details (address, decimals, symbol). Use to resolve symbol to address before lifi_get_quote.",
				Parameters: jsonschema.Definition{
					Type: jsonschema.Object,
					Properties: map[string]jsonschema.Definition{
						"chain": {Type: jsonschema.Integer, Description: "Chain ID"},
						"token": {Type: jsonschema.String, Description: "Token symbol (e.g. USDC) or contract address"},
					},
					Required: []string{"chain", "token"},
				},
			},
		},
	}
}

// IsAllowedForSubagent returns true if the tool can be used by stateless sub-agents.
func IsAllowedForSubagent(name string) bool {
	return ReadOnlyToolNames[name]
}

// DefinitionsForSubagent returns only read-only tools (read_file, web_search, read_memory)
// for stateless sub-agents that must not write session, memory, or reminders.
func DefinitionsForSubagent() []openai.Tool {
	all := Definitions()
	out := make([]openai.Tool, 0, len(ReadOnlyToolNames))
	for _, t := range all {
		if t.Function != nil && ReadOnlyToolNames[t.Function.Name] {
			out = append(out, t)
		}
	}
	return out
}

// ExecuteTool runs the named tool with the given JSON arguments and returns the result.
// For save_memory and read_memory, platform and userID must be injected by the caller via InjectMemoryArgs.
func (t *Tools) ExecuteTool(name, argsJSON string) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	strArgs := toStringMap(args)

	switch name {
	case "run_command":
		return runCommand(strArgs["command"])
	case "read_file":
		return readFile(strArgs["path"])
	case "write_file":
		return writeFile(strArgs["path"], strArgs["content"])
	case "web_search":
		return t.webSearch(strArgs["query"])
	case "save_memory":
		return t.saveMemory(strArgs)
	case "read_memory":
		return t.readMemory(strArgs, args)
	case "create_scheduled_reminder":
		return t.createScheduledReminder(strArgs)
	case "list_reminders":
		return t.listReminders(strArgs)
	case "delete_reminder":
		return t.deleteReminder(strArgs)
	case "http_request":
		return t.httpRequest(strArgs, args)
	case "lifi_get_quote":
		return t.lifiGetQuote(strArgs, args)
	case "lifi_track_status":
		return t.lifiTrackStatus(strArgs)
	case "lifi_check_route":
		return t.lifiCheckRoute(strArgs)
	case "lifi_get_token":
		return t.lifiGetToken(strArgs)
	case "x402_get_stats":
		return t.x402GetStats()
	case "wallet_get_balance":
		return t.walletGetBalance(strArgs, args)
	case "wallet_execute_transfer":
		return t.walletExecuteTransfer(strArgs, args)
	case "wallet_execute_contract_call":
		return t.walletExecuteContractCall(strArgs, args)
	case "wallet_list_transactions":
		return t.walletListTransactions(strArgs, args)
	case "wallet_get_portfolio":
		return t.walletGetPortfolio(strArgs, args)
	case "wallet_get_portfolio_value":
		return t.walletGetPortfolioValue(strArgs, args)
	case "wallet_get_activity":
		return t.walletGetActivity(strArgs, args)
	case "list_skills":
		return t.listSkills()
	case "read_skill":
		return t.readSkill(strArgs, args)
	case "read_skill_script":
		return t.readSkillScript(strArgs)
	case "write_skill":
		return t.writeSkill(strArgs, args)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func parseChainID(args map[string]string, rawArgs map[string]interface{}) int64 {
	if rawArgs != nil {
		if v := rawArgs["chain_id"]; v != nil {
			switch n := v.(type) {
			case float64:
				if n > 0 {
					return int64(n)
				}
			}
		}
	}
	if s := args["chain_id"]; s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func (t *Tools) walletGetBalance(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	if t.Wallet == nil {
		return "Wallet not configured. Set EVM_RPC_URL and WALLET_PRIVATE_KEY to enable.", nil
	}
	chainID := parseChainID(args, rawArgs)
	bal, err := t.Wallet.GetBalanceString(context.Background(), chainID, nil)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	ethHuman := weiToEth(bal)
	return fmt.Sprintf("Balance: %.6f ETH (%s wei)", ethHuman, bal), nil
}

func (t *Tools) walletExecuteTransfer(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	if t.Wallet == nil {
		return "Wallet not configured. Set EVM_RPC_URL and WALLET_PRIVATE_KEY to enable.", nil
	}
	platform := args["platform"]
	userID := args["user_id"]
	chatID := args["chat_id"]
	if platform == "" || userID == "" {
		return "Error: missing user context (platform, user_id).", nil
	}
	if chatID == "" {
		chatID = userID
	}
	chainID := parseChainID(args, rawArgs)
	return t.Wallet.ExecuteTransfer(context.Background(), chainID, args["to"], args["value_wei"], platform, userID, chatID)
}

func (t *Tools) walletExecuteContractCall(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	if t.Wallet == nil {
		return "Wallet not configured. Set EVM_RPC_URL and WALLET_PRIVATE_KEY to enable.", nil
	}
	platform := args["platform"]
	userID := args["user_id"]
	chatID := args["chat_id"]
	if platform == "" || userID == "" {
		return "Error: missing user context (platform, user_id).", nil
	}
	if chatID == "" {
		chatID = userID
	}
	valueWei := args["value_wei"]
	if valueWei == "" {
		valueWei = "0"
	}
	chainID := parseChainID(args, rawArgs)
	return t.Wallet.ExecuteContractCall(context.Background(), chainID, args["to"], args["data"], valueWei, platform, userID, chatID)
}

func (t *Tools) walletListTransactions(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	if t.Wallet == nil {
		return "Wallet not configured. Set EVM_RPC_URL and WALLET_PRIVATE_KEY to enable.", nil
	}
	chainID := parseChainID(args, rawArgs)
	limit := 20
	if rawArgs != nil {
		if v := rawArgs["limit"]; v != nil {
			switch n := v.(type) {
			case float64:
				if n > 0 && n <= 100 {
					limit = int(n)
				}
			}
		}
	}
	if s := args["limit"]; s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	return t.Wallet.ListTransactions(chainID, limit)
}

func (t *Tools) walletGetPortfolio(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	if t.Alchemy == nil || t.Wallet == nil {
		return "Portfolio tools require Alchemy (ALCHEMY_API_KEY or ALCHEMY_BASE_URL) and wallet. Set both to enable.", nil
	}
	chainID := parseChainID(args, rawArgs)
	includeZero := false
	if rawArgs != nil {
		if v, ok := rawArgs["include_zero"].(bool); ok {
			includeZero = v
		}
	}
	chainIDs := t.Wallet.ChainIDs()
	if chainID > 0 {
		chainIDs = []int64{chainID}
	}
	if len(chainIDs) == 0 {
		chainIDs = []int64{t.Wallet.DefaultChainID()}
	}
	var b strings.Builder
	addr := t.Wallet.WalletAddress()
	for _, cid := range chainIDs {
		// Native balance from wallet
		nativeBal, err := t.Wallet.GetBalanceString(context.Background(), cid, nil)
		if err != nil {
			b.WriteString(fmt.Sprintf("Chain %d: error %v\n", cid, err))
			continue
		}
		if chainID == 0 {
			b.WriteString(fmt.Sprintf("Chain %d:\n", cid))
		}
		ethHuman := weiToEth(nativeBal)
		b.WriteString(fmt.Sprintf("  Native: %.6f ETH (%s wei)\n", ethHuman, nativeBal))
		// ERC-20 from Alchemy
		tb, err := t.Alchemy.GetTokenBalances(context.Background(), cid, addr, "erc20")
		if err != nil {
			b.WriteString(fmt.Sprintf("  ERC-20: error %v\n", err))
			continue
		}
		for _, tkn := range tb.TokenBalances {
			if tkn.Error != "" {
				continue
			}
			if tkn.TokenBalance == "" || tkn.TokenBalance == "0x0" || tkn.TokenBalance == "0" {
				if !includeZero {
					continue
				}
			}
			meta, _ := t.Alchemy.GetTokenMetadata(context.Background(), cid, tkn.ContractAddress)
			symbol := "?"
			decimals := 18
			if meta != nil {
				symbol = meta.Symbol
				decimals = meta.Decimals
			}
			raw := tkn.TokenBalance
			if raw == "" || raw == "0x0" {
				raw = "0"
			}
			b.WriteString(fmt.Sprintf("  %s (%s): %s (decimals %d)\n", symbol, tkn.ContractAddress, raw, decimals))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), nil
}

func (t *Tools) walletGetPortfolioValue(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	if t.Alchemy == nil || t.Wallet == nil {
		return "Portfolio tools require Alchemy (ALCHEMY_API_KEY or ALCHEMY_BASE_URL) and wallet. Set both to enable.", nil
	}
	chainID := parseChainID(args, rawArgs)
	includeZero := false
	if rawArgs != nil {
		if v, ok := rawArgs["include_zero"].(bool); ok {
			includeZero = v
		}
	}
	chainIDs := t.Wallet.ChainIDs()
	if chainID > 0 {
		chainIDs = []int64{chainID}
	}
	if len(chainIDs) == 0 {
		chainIDs = []int64{t.Wallet.DefaultChainID()}
	}
	var b strings.Builder
	addr := t.Wallet.WalletAddress()
	totalUSD := 0.0
	for _, cid := range chainIDs {
		b.WriteString(fmt.Sprintf("Chain %d:\n", cid))
		// Native balance
		nativeBal, err := t.Wallet.GetBalanceString(context.Background(), cid, nil)
		if err != nil {
			b.WriteString(fmt.Sprintf("  Native: error %v\n", err))
			continue
		}
		nativeWei, _ := new(big.Int).SetString(nativeBal, 10)
		ethHuman := weiToEth(nativeBal)
		ethPrice, _ := t.Alchemy.GetTokenPrice(context.Background(), cid, "0x0000000000000000000000000000000000000000")
		if ethPrice > 0 {
			ethVal := new(big.Float).SetInt(nativeWei)
			ethVal.Quo(ethVal, big.NewFloat(1e18))
			ethVal.Mul(ethVal, big.NewFloat(ethPrice))
			f, _ := ethVal.Float64()
			totalUSD += f
			b.WriteString(fmt.Sprintf("  Native: %.6f ETH (%s wei) ≈ $%.2f\n", ethHuman, nativeBal, f))
		} else {
			b.WriteString(fmt.Sprintf("  Native: %.6f ETH (%s wei)\n", ethHuman, nativeBal))
		}
		// ERC-20
		tb, err := t.Alchemy.GetTokenBalances(context.Background(), cid, addr, "erc20")
		if err != nil {
			b.WriteString(fmt.Sprintf("  ERC-20: error %v\n", err))
			continue
		}
		for _, tkn := range tb.TokenBalances {
			if tkn.Error != "" {
				continue
			}
			raw := tkn.TokenBalance
			if raw == "" || raw == "0x0" {
				raw = "0"
			}
			balWei, ok := new(big.Int).SetString(strings.TrimPrefix(raw, "0x"), 16)
			if !ok {
				balWei, _ = new(big.Int).SetString(raw, 10)
			}
			if balWei.Sign() == 0 && !includeZero {
				continue
			}
			meta, _ := t.Alchemy.GetTokenMetadata(context.Background(), cid, tkn.ContractAddress)
			symbol := "?"
			decimals := 18
			if meta != nil {
				symbol = meta.Symbol
				decimals = meta.Decimals
			}
			price, _ := t.Alchemy.GetTokenPrice(context.Background(), cid, tkn.ContractAddress)
			// Fallback: known stablecoins often lack price data on some chains; assume $1
			if price == 0 && isStablecoin(symbol) {
				price = 1.0
			}
			human := new(big.Float).SetInt(balWei)
			human.Quo(human, big.NewFloat(float64(pow10(decimals))))
			humanF, _ := human.Float64()
			valUSD := humanF * price
			if price > 0 {
				totalUSD += valUSD
				b.WriteString(fmt.Sprintf("  %s: %.4f ≈ $%.2f\n", symbol, humanF, valUSD))
			} else {
				b.WriteString(fmt.Sprintf("  %s: %.4f (no price)\n", symbol, humanF))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf("Total ≈ $%.2f\n", totalUSD))
	return strings.TrimSpace(b.String()), nil
}

// weiToEth converts wei string to ETH (human-readable) as float64.
func weiToEth(weiStr string) float64 {
	w, ok := new(big.Int).SetString(weiStr, 10)
	if !ok {
		return 0
	}
	f := new(big.Float).SetInt(w)
	f.Quo(f, big.NewFloat(1e18))
	out, _ := f.Float64()
	return out
}

func pow10(n int) int64 {
	if n <= 0 {
		return 1
	}
	x := int64(1)
	for i := 0; i < n; i++ {
		x *= 10
	}
	return x
}

// isStablecoin returns true for common USD-pegged tokens (used when GetTokenPrice returns 0).
func isStablecoin(symbol string) bool {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	return s == "USDC" || s == "USDT" || s == "DAI" || s == "BUSD" || s == "FRAX" || s == "TUSD"
}

func (t *Tools) walletGetActivity(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	if t.Alchemy == nil || t.Wallet == nil {
		return "Activity tools require Alchemy and wallet. Set both to enable.", nil
	}
	chainID := parseChainID(args, rawArgs)
	if chainID == 0 {
		chainID = t.Wallet.DefaultChainID()
	}
	limit := 20
	if rawArgs != nil {
		if v, ok := rawArgs["limit"].(float64); ok && v > 0 && v <= 100 {
			limit = int(v)
		}
	}
	addr := t.Wallet.WalletAddress()
	// "internal" category is only supported for ETH (1) and Polygon (137); omit for other chains
	categories := []string{"external", "erc20"}
	if chainID == 1 || chainID == 137 {
		categories = append(categories, "internal")
	}
	// Fetch incoming (toAddress) and outgoing (fromAddress); merge
	resIn, err := t.Alchemy.GetAssetTransfers(context.Background(), chainID, "", addr, categories, limit, "")
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	resOut, err := t.Alchemy.GetAssetTransfers(context.Background(), chainID, addr, "", categories, limit, "")
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	seen := make(map[string]bool)
	var transfers []alchemy.AssetTransfer
	for _, tr := range resIn.Transfers {
		if !seen[tr.Hash] {
			seen[tr.Hash] = true
			transfers = append(transfers, tr)
		}
	}
	for _, tr := range resOut.Transfers {
		if !seen[tr.Hash] {
			seen[tr.Hash] = true
			transfers = append(transfers, tr)
		}
	}
	if len(transfers) == 0 {
		return "No transfers found.", nil
	}
	var b strings.Builder
	for _, tr := range transfers {
		ts := ""
		if tr.Metadata != nil && tr.Metadata.BlockTimestamp != "" {
			ts = " " + tr.Metadata.BlockTimestamp
		}
		b.WriteString(fmt.Sprintf("[%s] %s %s → %s value=%s asset=%s%s\n",
			tr.Category, tr.Hash, tr.From, tr.To, tr.Value, tr.Asset, ts))
	}
	return strings.TrimSpace(b.String()), nil
}

func (t *Tools) listSkills() (string, error) {
	if t.Skills == nil {
		return "Skills not configured. Set SKILLS_DIR to enable.", nil
	}
	list, err := t.Skills.List()
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if len(list) == 0 {
		return "No skills installed. Use newSkill to add one.", nil
	}
	var b strings.Builder
	for _, s := range list {
		b.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
	}
	return strings.TrimSpace(b.String()), nil
}

func (t *Tools) readSkill(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	if t.Skills == nil {
		return "Skills not configured.", nil
	}
	name := strings.TrimSpace(args["name"])
	if name == "" {
		return "Error: name is required.", nil
	}
	full := true
	if rawArgs != nil {
		if v, ok := rawArgs["full"].(bool); ok {
			full = v
		}
	}
	skill, err := t.Skills.Get(name)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if !full {
		return fmt.Sprintf("name: %s\ndescription: %s", skill.Name, skill.Description), nil
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("name: %s\n", skill.Name))
	b.WriteString(fmt.Sprintf("description: %s\n\n", skill.Description))
	b.WriteString("--- body ---\n")
	b.WriteString(skill.Body)
	if len(skill.Scripts) > 0 {
		b.WriteString("\n\n--- scripts ---\n")
		for _, s := range skill.Scripts {
			b.WriteString(fmt.Sprintf("- %s (%s)\n", s.Path, s.Language))
		}
	}
	return b.String(), nil
}

func (t *Tools) readSkillScript(args map[string]string) (string, error) {
	if t.Skills == nil {
		return "Skills not configured.", nil
	}
	name := strings.TrimSpace(args["name"])
	path := strings.TrimSpace(args["path"])
	if name == "" || path == "" {
		return "Error: name and path are required.", nil
	}
	content, err := t.Skills.ReadScript(name, path)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	return content, nil
}

func (t *Tools) writeSkill(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	sw, ok := t.Skills.(SkillsWriter)
	if !ok || sw == nil {
		return "Skills write not configured.", nil
	}
	name := strings.TrimSpace(args["name"])
	skillMd := args["skill_md"]
	if name == "" || skillMd == "" {
		return "Error: name and skill_md are required.", nil
	}
	scripts := make(map[string]string)
	if rawArgs != nil {
		if m, ok := rawArgs["scripts"].(map[string]interface{}); ok {
			for k, v := range m {
				if s, ok := v.(string); ok {
					scripts[k] = s
				}
			}
		}
	}
	// Run security and feasibility checks when LLM client is available
	if t.LLMClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		secResult, err := skills.RunSecurityCheck(ctx, t.LLMClient, skillMd, scripts)
		if err != nil {
			return "Security check failed: " + err.Error(), nil
		}
		if !secResult.Safe || secResult.Severity == "high" {
			return "Skill rejected for security reasons:\n" + strings.Join(secResult.Issues, "\n") + "\n\nPlease revise and try again.", nil
		}
		feasResult, err := skills.RunFeasibilityCheck(ctx, t.LLMClient, skillMd, scripts)
		if err != nil {
			return "Feasibility check failed: " + err.Error(), nil
		}
		if !feasResult.Clear {
			return "Skill has clarity or feasibility issues:\n" + strings.Join(feasResult.Issues, "\n") + "\n\nPlease revise and try again.", nil
		}
	}
	if err := sw.WriteSkill(name, skillMd, scripts); err != nil {
		return "Error: " + err.Error(), nil
	}
	return fmt.Sprintf("Skill %q saved to %s.", name, filepath.Join(sw.Dir(), name)), nil
}

// InjectWalletArgs adds platform, user_id, chat_id to args for wallet tools.
func InjectWalletArgs(argsJSON, platform, userID, chatID string) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	args["platform"] = platform
	args["user_id"] = userID
	args["chat_id"] = chatID
	b, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// TryWalletApproval attempts to execute a wallet approval by ID. Returns (true, result) if handled.
func (t *Tools) TryWalletApproval(ctx context.Context, cmd, platform, userID, chatID string) (bool, string) {
	if t.Wallet == nil {
		return false, ""
	}
	if !strings.HasPrefix(strings.TrimSpace(cmd), "tx_") {
		return false, ""
	}
	result, err := t.Wallet.ExecuteApproved(ctx, strings.TrimSpace(cmd), platform, userID, chatID)
	if err != nil {
		return true, "Approval failed: " + err.Error()
	}
	return true, result
}

func toStringMap(m map[string]interface{}) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v == nil {
			continue
		}
		switch val := v.(type) {
		case string:
			out[k] = val
		case float64:
			out[k] = strconv.FormatFloat(val, 'f', -1, 64)
		case int:
			out[k] = strconv.Itoa(val)
		}
	}
	return out
}

// InjectMemoryArgs adds platform and user_id to args for memory tools. Call before ExecuteTool.
func InjectMemoryArgs(argsJSON, platform, userID string) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	args["platform"] = platform
	args["user_id"] = userID
	b, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// InjectReminderArgs adds platform, user_id, and chat_id to args for reminder tools.
func InjectReminderArgs(argsJSON, platform, userID, chatID string) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	args["platform"] = platform
	args["user_id"] = userID
	args["chat_id"] = chatID
	b, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (t *Tools) saveMemory(args map[string]string) (string, error) {
	if t.MemoryStore == nil {
		return "Memory not configured.", nil
	}
	content := args["content"]
	platform := args["platform"]
	userID := args["user_id"]
	if platform == "" || userID == "" {
		return "Error: missing user context.", nil
	}
	if strings.TrimSpace(content) == "" {
		return "Error: content cannot be empty.", nil
	}
	if err := t.MemoryStore.Save(platform, userID, content, args["tags"]); err != nil {
		return "Error: " + err.Error(), nil
	}
	return "Memory saved.", nil
}

func (t *Tools) readMemory(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	if t.MemoryStore == nil {
		return "Memory not configured.", nil
	}
	query := args["query"]
	platform := args["platform"]
	userID := args["user_id"]
	if platform == "" || userID == "" {
		return "Error: missing user context.", nil
	}
	limit := 5
	if v := rawArgs["limit"]; v != nil {
		switch n := v.(type) {
		case float64:
			if n > 0 && n <= 20 {
				limit = int(n)
			}
		case string:
			if k, err := strconv.Atoi(n); err == nil && k > 0 && k <= 20 {
				limit = k
			}
		}
	} else if s := args["limit"]; s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 20 {
			limit = n
		}
	}
	memories, err := t.MemoryStore.Search(platform, userID, query, limit)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if len(memories) == 0 {
		return "No relevant memories found.", nil
	}
	var b strings.Builder
	for i, m := range memories {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(fmt.Sprintf("- %s", m.Content))
		if m.Tags != "" {
			b.WriteString(fmt.Sprintf(" [%s]", m.Tags))
		}
	}
	return b.String(), nil
}

func (t *Tools) createScheduledReminder(args map[string]string) (string, error) {
	if t.ReminderStore == nil {
		return "Reminders not configured.", nil
	}
	schedule := args["schedule"]
	message := args["message"]
	platform := args["platform"]
	userID := args["user_id"]
	chatID := args["chat_id"]
	if platform == "" || userID == "" {
		return "Error: missing user context.", nil
	}
	if strings.TrimSpace(schedule) == "" || strings.TrimSpace(message) == "" {
		return "Error: schedule and message are required.", nil
	}
	if chatID == "" {
		chatID = userID
	}
	id, err := t.ReminderStore.Add(platform, userID, chatID, schedule, message)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	return fmt.Sprintf("Reminder created (ID: %s). You'll receive \"%s\" on schedule: %s", id, message, schedule), nil
}

func (t *Tools) listReminders(args map[string]string) (string, error) {
	if t.ReminderStore == nil {
		return "Reminders not configured.", nil
	}
	platform := args["platform"]
	userID := args["user_id"]
	if platform == "" || userID == "" {
		return "Error: missing user context.", nil
	}
	list, err := t.ReminderStore.ListForPlatform(platform, userID)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if len(list) == 0 {
		return "No reminders scheduled.", nil
	}
	var b strings.Builder
	for i, r := range list {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("- [%s] %s → \"%s\" (schedule: %s)", r.ID, r.CreatedAt.Format("2006-01-02"), r.Message, r.Schedule))
	}
	return b.String(), nil
}

func (t *Tools) deleteReminder(args map[string]string) (string, error) {
	if t.ReminderStore == nil {
		return "Reminders not configured.", nil
	}
	id := args["id"]
	if strings.TrimSpace(id) == "" {
		return "Error: reminder ID is required.", nil
	}
	if err := t.ReminderStore.Delete(id, args["platform"], args["user_id"]); err != nil {
		return "Error: " + err.Error(), nil
	}
	return "Reminder deleted.", nil
}

const (
	httpRequestMaxBody  = 64 * 1024 // 64KB
	httpRequestTimeout  = 30 * time.Second
	httpRequestRedactHd = "authorization,cookie,x-api-key,x-auth-token,proxy-authorization,set-cookie,x-csrf-token,x-session-id"
	lifiBaseURL         = "https://li.quest/v1"
	lifiTimeout         = 30 * time.Second
)

// allowLoopbackForTesting enables loopback in isURLAllowed for httptest-based tests. Do not set in production.
var allowLoopbackForTesting bool

// isURLAllowed rejects URLs that could target internal/private services (SSRF mitigation).
func isURLAllowed(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme %q not allowed", scheme)
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		if !allowLoopbackForTesting {
			return fmt.Errorf("localhost/internal hostnames blocked")
		}
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			if ip4[0] == 127 {
				if !allowLoopbackForTesting {
					return fmt.Errorf("loopback blocked")
				}
				continue
			}
			if ip4[0] == 10 ||
				(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
				(ip4[0] == 192 && ip4[1] == 168) ||
				(ip4[0] == 169 && ip4[1] == 254) {
				return fmt.Errorf("private IP blocked")
			}
		} else {
			if ip.Equal(net.IPv6loopback) {
				return fmt.Errorf("loopback blocked")
			}
			if len(ip) >= 2 && ((ip[0] == 0xfe && (ip[1]&0xc0) == 0x80) || ip[0] == 0xfc || ip[0] == 0xfd) {
				return fmt.Errorf("private IPv6 blocked")
			}
		}
	}
	return nil
}

func (t *Tools) httpRequest(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	rawURL := strings.TrimSpace(args["url"])
	if rawURL == "" {
		return "Error: url is required.", nil
	}
	if _, err := url.Parse(rawURL); err != nil {
		return "Error: invalid url: " + err.Error(), nil
	}
	if err := isURLAllowed(rawURL); err != nil {
		return "Error: URL not allowed (blocked internal/private addresses).", nil
	}
	method := strings.TrimSpace(strings.ToUpper(args["method"]))
	if method == "" {
		method = "GET"
	}
	bodyStr := args["body"]

	var body io.Reader
	if bodyStr != "" && (method == "POST" || method == "PUT" || method == "PATCH") {
		body = strings.NewReader(bodyStr)
	}

	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	req.Header.Set("User-Agent", "custom-agent/1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Optional headers from args
	if rawArgs != nil {
		if h, ok := rawArgs["headers"].(map[string]interface{}); ok {
			for k, v := range h {
				if s, ok := v.(string); ok && k != "" {
					req.Header.Set(k, s)
				}
			}
		}
	}

	client := http.DefaultClient
	if t.X402Client != nil {
		client = t.X402Client.Client
	}
	client = &http.Client{
		Transport: client.Transport,
		Timeout:   httpRequestTimeout,
	}

	ctx, cancel := context.WithTimeout(context.Background(), httpRequestTimeout)
	defer cancel()
	req = req.WithContext(ctx)

	log.Printf("[http_request] %s %s | body: %s", method, rawURL, redact.Redact(bodyStr))
	resp, err := client.Do(req)
	if err != nil {
		return "Error: request failed: " + err.Error(), nil
	}
	defer resp.Body.Close()

	// Cap body read
	limited := io.LimitReader(resp.Body, httpRequestMaxBody+1)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return "Error: failed to read response: " + err.Error(), nil
	}

	truncated := len(respBody) > httpRequestMaxBody
	if truncated {
		respBody = respBody[:httpRequestMaxBody]
	}

	// Redact sensitive headers
	redactSet := make(map[string]bool)
	for _, h := range strings.Split(strings.ToLower(httpRequestRedactHd), ",") {
		redactSet[strings.TrimSpace(h)] = true
	}
	var safeHeaders []string
	for k, v := range resp.Header {
		lower := strings.ToLower(k)
		if redactSet[lower] {
			safeHeaders = append(safeHeaders, k+": [redacted]")
		} else if len(v) > 0 {
			safeHeaders = append(safeHeaders, k+": "+v[0])
		}
	}

	var out strings.Builder
	out.WriteString(fmt.Sprintf("Status: %d %s\n", resp.StatusCode, resp.Status))
	if len(safeHeaders) > 0 {
		out.WriteString("Headers:\n")
		for _, h := range safeHeaders {
			out.WriteString("  " + h + "\n")
		}
	}
	out.WriteString("Body:\n")
	out.WriteString(redact.Redact(string(respBody)))
	if truncated {
		out.WriteString("\n\n[truncated]")
	}
	if resp.StatusCode == http.StatusPaymentRequired && t.X402Client == nil {
		out.WriteString("\n\nNote: 402 Payment Required. Configure wallet (EVM_RPC_URL, WALLET_PRIVATE_KEY) for x402 to pay automatically.")
	}
	return out.String(), nil
}

func lifiGet(path string, params url.Values) (string, error) {
	rawURL := lifiBaseURL + path
	if len(params) > 0 {
		rawURL += "?" + params.Encode()
	}
	doReq := func() (string, int, error) {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return "", 0, err
		}
		req.Header.Set("User-Agent", "custom-agent/1.0")
		ctx, cancel := context.WithTimeout(context.Background(), lifiTimeout)
		defer cancel()
		req = req.WithContext(ctx)
		log.Printf("[lifi] GET %s", rawURL)
		client := &http.Client{Timeout: lifiTimeout}
		resp, err := client.Do(req)
		if err != nil {
			return "", 0, err
		}
		defer resp.Body.Close()
		limited := io.LimitReader(resp.Body, httpRequestMaxBody+1)
		body, err := io.ReadAll(limited)
		if err != nil {
			return "", resp.StatusCode, err
		}
		if len(body) > httpRequestMaxBody {
			body = body[:httpRequestMaxBody]
		}
		return string(body), resp.StatusCode, nil
	}
	body, status, err := doReq()
	if err != nil {
		return "Error: LI.FI request failed: " + err.Error(), nil
	}
	if status == http.StatusTooManyRequests {
		log.Printf("[lifi] 429 rate limited, retrying after 30s")
		time.Sleep(30 * time.Second)
		body, status, err = doReq()
		if err != nil {
			return "Error: LI.FI request failed (retry): " + err.Error(), nil
		}
	}
	out := fmt.Sprintf("Status: %d %s\nBody:\n%s", status, http.StatusText(status), body)
	if status >= 400 {
		out += "\n\nLI.FI API error. Check params (chain IDs as integers, token addresses, from_amount in wei)."
	}
	return out, nil
}

func (t *Tools) lifiGetQuote(args map[string]string, rawArgs map[string]interface{}) (string, error) {
	fromChain := strings.TrimSpace(args["from_chain"])
	toChain := strings.TrimSpace(args["to_chain"])
	fromToken := strings.TrimSpace(args["from_token"])
	toToken := strings.TrimSpace(args["to_token"])
	fromAmount := strings.TrimSpace(args["from_amount"])
	if fromChain == "" || toChain == "" || fromToken == "" || toToken == "" || fromAmount == "" {
		return "Error: lifi_get_quote requires from_chain, to_chain, from_token, to_token, from_amount.", nil
	}
	fromAddress := strings.TrimSpace(args["from_address"])
	if fromAddress == "" && t.Wallet != nil {
		fromAddress = t.Wallet.WalletAddress()
	}
	if fromAddress == "" {
		return "Error: lifi_get_quote requires from_address when wallet is not configured.", nil
	}
	params := url.Values{}
	params.Set("fromChain", fromChain)
	params.Set("toChain", toChain)
	params.Set("fromToken", fromToken)
	params.Set("toToken", toToken)
	params.Set("fromAddress", fromAddress)
	params.Set("fromAmount", fromAmount)
	if s := strings.TrimSpace(args["to_address"]); s != "" {
		params.Set("toAddress", s)
	}
	if s := strings.TrimSpace(args["slippage"]); s != "" {
		params.Set("slippage", s)
	} else {
		params.Set("slippage", "0.03")
	}
	if s := strings.TrimSpace(args["order"]); s != "" {
		params.Set("order", s)
	} else {
		params.Set("order", "RECOMMENDED")
	}
	return lifiGet("/quote", params)
}

func (t *Tools) lifiTrackStatus(args map[string]string) (string, error) {
	txHash := strings.TrimSpace(args["tx_hash"])
	if txHash == "" {
		return "Error: lifi_track_status requires tx_hash.", nil
	}
	params := url.Values{}
	params.Set("txHash", txHash)
	if s := strings.TrimSpace(args["bridge"]); s != "" {
		params.Set("bridge", s)
	}
	if s := strings.TrimSpace(args["from_chain"]); s != "" {
		params.Set("fromChain", s)
	}
	if s := strings.TrimSpace(args["to_chain"]); s != "" {
		params.Set("toChain", s)
	}
	return lifiGet("/status", params)
}

func (t *Tools) lifiCheckRoute(args map[string]string) (string, error) {
	fromChain := strings.TrimSpace(args["from_chain"])
	toChain := strings.TrimSpace(args["to_chain"])
	fromToken := strings.TrimSpace(args["from_token"])
	if fromChain == "" || toChain == "" || fromToken == "" {
		return "Error: lifi_check_route requires from_chain, to_chain, from_token.", nil
	}
	params := url.Values{}
	params.Set("fromChain", fromChain)
	params.Set("toChain", toChain)
	params.Set("fromToken", fromToken)
	if s := strings.TrimSpace(args["to_token"]); s != "" {
		params.Set("toToken", s)
	}
	return lifiGet("/connections", params)
}

func (t *Tools) lifiGetToken(args map[string]string) (string, error) {
	chain := strings.TrimSpace(args["chain"])
	token := strings.TrimSpace(args["token"])
	if chain == "" || token == "" {
		return "Error: lifi_get_token requires chain and token.", nil
	}
	params := url.Values{}
	params.Set("chain", chain)
	params.Set("token", token)
	return lifiGet("/token", params)
}

func (t *Tools) x402GetStats() (string, error) {
	// When SpendStore is set (autonomous mode), return locally tracked stats instead of global router stats.
	if t.SpendStore != nil {
		totalUSD, totalTokens := t.SpendStore.Stats()
		return fmt.Sprintf("total_spent_usd=%.4f total_tokens=%d (locally tracked; actual inference spend for this wallet)",
			totalUSD, totalTokens), nil
	}
	if t.X402Client == nil || t.X402RouterURL == "" {
		return "x402_get_stats requires autonomous mode with x402 router configured (X402_ROUTER_URL).", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stats, err := t.X402Client.FetchRouterStats(ctx, t.X402RouterURL)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	capUSD := 0.0
	if t.X402PermitCap != "" {
		capUSD, _ = strconv.ParseFloat(t.X402PermitCap, 64)
	}
	spentUSD, _ := strconv.ParseFloat(stats.TotalSpentUSD, 64)
	remaining := capUSD - spentUSD
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf("total_spent_usd=%s total_tokens=%d permit_cap_usd=%s remaining_usd≈%.2f",
		stats.TotalSpentUSD, stats.TotalTokens, t.X402PermitCap, remaining), nil
}

func runCommand(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("empty command")
	}

	if IsBlocked(command) {
		return "Permission denied: command is blocked for safety.", nil
	}

	if IsSafe(command) {
		return executeCommand(command)
	}

	approved, err := LoadApprovals()
	if err != nil {
		return "", fmt.Errorf("failed to load approvals: %w", err)
	}
	if IsApproved(command, approved) {
		return executeCommand(command)
	}

	return "Permission denied. User can approve by saying 'approve: " + command + "' in chat.", nil
}

func executeCommand(command string) (string, error) {
	// #nosec G204 -- run_command tool; blocked patterns, safe list, and approval mitigate injection
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = getWorkDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if stderr.Len() > 0 {
		out += "\nstderr: " + strings.TrimSpace(stderr.String())
	}
	if err != nil {
		return out, fmt.Errorf("command failed: %w", err)
	}
	if out == "" {
		return "(no output)", nil
	}
	return out, nil
}

func readFile(path string) (string, error) {
	resolved, err := resolvePathSafe(path)
	if err != nil {
		return "", err
	}
	// #nosec G304 -- path validated by resolvePathSafe
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read failed: %w", err)
	}
	return string(data), nil
}

func writeFile(path, content string) (string, error) {
	resolved, err := resolvePathSafe(path)
	if err != nil {
		return "", err
	}
	// #nosec G301 -- 0750 restricts to owner+group
	if err := os.MkdirAll(filepath.Dir(resolved), 0750); err != nil {
		return "", fmt.Errorf("mkdir failed: %w", err)
	}
	// #nosec G306 -- 0600 restricts to owner
	if err := os.WriteFile(resolved, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("write failed: %w", err)
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), resolved), nil
}

func (t *Tools) webSearch(query string) (string, error) {
	if t.BraveSearchAPIKey == "" {
		return "", fmt.Errorf("BRAVE_SEARCH_API_KEY not configured")
	}

	u := "https://api.search.brave.com/res/v1/web/search?q=" + url.QueryEscape(query)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	req.Header.Set("X-Subscription-Token", t.BraveSearchAPIKey)

	log.Printf("[web_search] GET %s", u)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search failed: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parse failed: %w", err)
	}

	if len(result.Web.Results) == 0 {
		return "No results found. Try a different query.", nil
	}

	var out strings.Builder
	for i, r := range result.Web.Results {
		if i >= 5 {
			break
		}
		if i > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(fmt.Sprintf("%d. %s\n   %s\n   %s", i+1, r.Title, r.URL, r.Description))
	}
	return out.String(), nil
}

func getWorkDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func resolvePathSafe(path string) (string, error) {
	base := getWorkDir()
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("invalid base path: %w", err)
	}
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	var resolved string
	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else {
		resolved = filepath.Clean(filepath.Join(base, path))
	}
	resolvedAbs, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	rel, err := filepath.Rel(baseAbs, resolvedAbs)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes allowed directory")
	}
	return resolvedAbs, nil
}
