package spend

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	spendDir    = "spend"
	spendFile   = "global.jsonl"
	fallbackIn  = 1.0 // USD per 1M input tokens
	fallbackOut = 2.0 // USD per 1M output tokens
)

// Entry is a single inference call record.
type Entry struct {
	Model            string  `json:"model"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	Timestamp        string  `json:"ts"`
}

// modelPricing holds input/output cost per 1M tokens (USD).
type modelPricing struct {
	InputPer1M  float64
	OutputPer1M float64
}

// Store persists inference spend to disk and provides stats.
type Store struct {
	mu         sync.RWMutex
	dir        string
	modelsPath string
	pricing    map[string]modelPricing
}

// modelsJSON is the structure of skills-data/x402-router/models.json
type modelsJSON struct {
	Data []struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Pricing  struct {
			InputPer1M  string `json:"input_per_1m"`
			OutputPer1M string `json:"output_per_1m"`
		} `json:"pricing"`
	} `json:"data"`
}

// NewStore creates a spend store. modelsPath is the directory containing x402-router/models.json (e.g. skills-data).
func NewStore(modelsPath string) *Store {
	s := &Store{
		dir:        spendDir,
		modelsPath: filepath.Join(modelsPath, "x402-router", "models.json"),
		pricing:    make(map[string]modelPricing),
	}
	s.loadPricing()
	return s
}

func (s *Store) loadPricing() {
	data, err := os.ReadFile(s.modelsPath)
	if err != nil {
		return
	}
	var m modelsJSON
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	for _, d := range m.Data {
		in, _ := strconv.ParseFloat(d.Pricing.InputPer1M, 64)
		out, _ := strconv.ParseFloat(d.Pricing.OutputPer1M, 64)
		p := modelPricing{InputPer1M: in, OutputPer1M: out}
		// Build keys: provider:id, provider::id
		key1 := d.Provider + ":" + d.ID
		key2 := d.Provider + "::" + d.ID
		s.pricing[key1] = p
		s.pricing[key2] = p
		s.pricing[d.Provider+"/"+d.ID] = p
	}
}

func (s *Store) priceForModel(model string) (in, out float64) {
	// Try exact match first
	if p, ok := s.pricing[model]; ok {
		return p.InputPer1M, p.OutputPer1M
	}
	// Normalize: "openai:gpt-5-mini" -> try "openai:gpt-5-mini", "openai::gpt-5-mini"
	parts := strings.SplitN(model, ":", 2)
	if len(parts) == 2 {
		alt := parts[0] + "::" + parts[1]
		if p, ok := s.pricing[alt]; ok {
			return p.InputPer1M, p.OutputPer1M
		}
	}
	// Try provider/id
	if len(parts) == 2 {
		alt := parts[0] + "/" + parts[1]
		if p, ok := s.pricing[alt]; ok {
			return p.InputPer1M, p.OutputPer1M
		}
	}
	return fallbackIn, fallbackOut
}

func (s *Store) path() string {
	return filepath.Join(s.dir, spendFile)
}

// Record appends a single inference call. Safe to call with nil store.
func (s *Store) Record(model string, promptTokens, completionTokens int) {
	if s == nil || (promptTokens == 0 && completionTokens == 0) {
		return
	}
	inPer1M, outPer1M := s.priceForModel(model)
	cost := (float64(promptTokens)/1e6)*inPer1M + (float64(completionTokens)/1e6)*outPer1M

	e := Entry{
		Model:            model,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CostUSD:          cost,
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return
	}
	f, err := os.OpenFile(s.path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	_ = enc.Encode(e)
}

// Stats returns total spent USD and total tokens.
func (s *Store) Stats() (totalSpentUSD float64, totalTokens int64) {
	if s == nil {
		return 0, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	f, err := os.Open(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0
		}
		return 0, 0
	}
	defer f.Close()

	var totalCost float64
	var totalTok int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		totalCost += e.CostUSD
		totalTok += int64(e.PromptTokens + e.CompletionTokens)
	}
	return totalCost, totalTok
}
