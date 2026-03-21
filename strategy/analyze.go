package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/semaphore"
)

const defaultConcurrency = 8

// TradeType mirrors STRATEGY.md trade categories for deployable base.
type TradeType string

const (
	TradeBuyWithUSDC     TradeType = "buy_with_usdc"
	TradeSwapAsset       TradeType = "swap_asset"
	TradeRebalanceAsset  TradeType = "rebalance_asset"
	TradeReserveRecovery TradeType = "reserve_recovery"
	TradeBootstrap       TradeType = "bootstrap"
	TradeOther           TradeType = "other"
)

// RiskTier for Kelly fraction.
type RiskTier string

const (
	RiskBluechip    RiskTier = "bluechip"
	RiskSpeculative RiskTier = "speculative"
)

// AnalyzeRequest is the JSON body for strategy_factor_analysis tool.
type AnalyzeRequest struct {
	SeriesJSON map[string]json.RawMessage `json:"series_json"`

	PortfolioSymbols []string `json:"portfolio_symbols"`
	TargetSymbol     string   `json:"target_symbol"`

	PortfolioValueUSD             float64 `json:"portfolio_value_usd"`
	DeployableUSDCAboveReserveUSD float64 `json:"deployable_usdc_above_reserve_usd"`
	SourceAsset                   string  `json:"source_asset"`
	SourceAssetValueUSD           float64 `json:"source_asset_value_usd"`
	TradeType                     string  `json:"trade_type"`
	RiskTier                      string  `json:"risk_tier"`
}

// AssetResult is one analyzed symbol.
type AssetResult struct {
	Symbol             string           `json:"symbol"`
	Factors            Factors          `json:"factors"`
	BenchmarkContext   BenchmarkContext `json:"benchmark_context"`
	SignalScore        float64          `json:"signal_score"`
	PWin               float64          `json:"p_win"`
	PayoffEstimate     float64          `json:"payoff_estimate"`
	LossEstimate       float64          `json:"loss_estimate"`
	EV                 float64          `json:"ev"`
	KellyFull          float64          `json:"kelly_full"`
	KellyFractionUsed  float64          `json:"kelly_fraction_used"`
	RecommendedSizeUSD float64          `json:"recommended_size_usd"`
	Go                 bool             `json:"go"`
	Reason             string           `json:"reason"`
}

// AnalyzeResponse is returned as JSON string from the tool.
type AnalyzeResponse struct {
	Benchmarks        map[string]Factors `json:"benchmarks"`
	Assets            []AssetResult      `json:"assets"`
	DeployableBaseUSD float64            `json:"deployable_base_usd"`
	Notes             string             `json:"notes,omitempty"`
}

// NormalizeSymbol maps common names to canonical keys for display.
func NormalizeSymbol(s string) string {
	u := strings.TrimSpace(strings.ToUpper(s))
	switch u {
	case "BITCOIN", "BTC":
		return "BTC"
	case "ETHEREUM", "ETH", "WETH":
		return "ETH"
	default:
		return u
	}
}

func canonicalBenchKey(sym string) string {
	u := strings.ToUpper(strings.TrimSpace(sym))
	switch u {
	case "BITCOIN", "BTC", "WBTC":
		return "BTC"
	case "ETHEREUM", "ETH", "WETH":
		return "ETH"
	default:
		return u
	}
}

// resolveSeriesKeys finds BTC/ETH series from flexible map keys (prefers "bitcoin"/"ethereum" labels).
func resolveSeriesKeys(series map[string]json.RawMessage) (btcKey, ethKey string) {
	var btcCand, ethCand []string
	for k := range series {
		switch canonicalBenchKey(k) {
		case "BTC":
			btcCand = append(btcCand, k)
		case "ETH":
			ethCand = append(ethCand, k)
		}
	}
	sort.Strings(btcCand)
	sort.Strings(ethCand)
	for _, k := range btcCand {
		if strings.EqualFold(strings.TrimSpace(k), "bitcoin") {
			btcKey = k
			break
		}
	}
	if btcKey == "" && len(btcCand) > 0 {
		btcKey = btcCand[0]
	}
	for _, k := range ethCand {
		if strings.EqualFold(strings.TrimSpace(k), "ethereum") {
			ethKey = k
			break
		}
	}
	if ethKey == "" && len(ethCand) > 0 {
		ethKey = ethCand[0]
	}
	return btcKey, ethKey
}

// DeployableBaseUSD picks sizing base per trade type.
func DeployableBaseUSD(req AnalyzeRequest) float64 {
	tt := TradeType(strings.TrimSpace(req.TradeType))
	switch tt {
	case TradeBuyWithUSDC, TradeBootstrap:
		if req.DeployableUSDCAboveReserveUSD > 0 {
			return req.DeployableUSDCAboveReserveUSD
		}
	case TradeSwapAsset, TradeRebalanceAsset:
		if req.SourceAssetValueUSD > 0 {
			return req.SourceAssetValueUSD
		}
	case TradeReserveRecovery:
		if req.SourceAssetValueUSD > 0 {
			return req.SourceAssetValueUSD
		}
	}
	if req.DeployableUSDCAboveReserveUSD > 0 {
		return req.DeployableUSDCAboveReserveUSD
	}
	if req.SourceAssetValueUSD > 0 {
		return req.SourceAssetValueUSD
	}
	return 0
}

func isSpeculativeTier(r string) bool {
	return strings.EqualFold(strings.TrimSpace(r), string(RiskSpeculative))
}

// Analyze runs multifactor + EV/Kelly for requested symbols (concurrent per asset).
func Analyze(ctx context.Context, req AnalyzeRequest) (*AnalyzeResponse, error) {
	if len(req.SeriesJSON) == 0 {
		return nil, fmt.Errorf("series_json is required (map symbol -> Tokenaru JSON or {\"closes\":[...]})")
	}
	sem := semaphore.NewWeighted(defaultConcurrency)
	var mu sync.Mutex
	closesMap := make(map[string][]float64)
	var firstErr error

	var wg sync.WaitGroup
	for sym, raw := range req.SeriesJSON {
		sym := sym
		raw := raw
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sem.Acquire(ctx, 1)
			defer sem.Release(1)
			c, err := ExtractCloses(raw)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", sym, err)
				}
				return
			}
			closesMap[sym] = c
		}()
	}
	wg.Wait()
	if len(closesMap) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("no valid series parsed")
	}

	btcKey, ethKey := resolveSeriesKeys(req.SeriesJSON)
	benchFactors := make(map[string]Factors)
	if btcKey != "" {
		if c := closesMap[btcKey]; len(c) > 0 {
			benchFactors["BTC"] = ComputeFactors(c)
		}
	}
	if ethKey != "" {
		if c := closesMap[ethKey]; len(c) > 0 {
			benchFactors["ETH"] = ComputeFactors(c)
		}
	}
	btcF := benchFactors["BTC"]
	ethF := benchFactors["ETH"]

	symbols := selectSymbols(req, closesMap, btcKey, ethKey)
	sort.Strings(symbols)

	deploy := DeployableBaseUSD(req)
	spec := isSpeculativeTier(req.RiskTier)
	port := req.PortfolioValueUSD
	if port <= 0 {
		port = deploy
		if port <= 0 {
			port = 1
		}
	}

	results := make([]AssetResult, 0, len(symbols))
	var resMu sync.Mutex
	wg = sync.WaitGroup{}
	for _, sym := range symbols {
		sym := sym
		c := closesMap[sym]
		if len(c) < 3 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sem.Acquire(ctx, 1)
			defer sem.Release(1)
			f := ComputeFactors(c)
			isBench := sym == btcKey || sym == ethKey
			bc := BenchmarkContextFor(f.Momentum7d, f.Momentum14d, btcF.Momentum7d, btcF.Momentum14d, ethF.Momentum7d, ethF.Momentum14d)
			sig := SignalScore(f, bc, isBench)
			p, payoff, loss := DeriveProbPayoffLoss(sig, f.Vol14d)
			ev := ExpectedValue(p, payoff, loss)
			kFull := KellyFraction(p, payoff, loss)
			kUsed := ApplyKellyFraction(kFull, spec)
			size := CapSizeUSD(kUsed, deploy, port, spec)
			goN := ev > 0 && size > 0 && kFull > 0
			reason := fmt.Sprintf("signal=%.3f ev=%.4f kelly_used=%.4f size=$%.2f", sig, ev, kUsed, size)
			if !goN {
				reason = fmt.Sprintf("NO-GO: ev=%.4f size=$%.2f kelly_full=%.4f", ev, size, kFull)
			}
			ar := AssetResult{
				Symbol:             sym,
				Factors:            f,
				BenchmarkContext:   bc,
				SignalScore:        sig,
				PWin:               p,
				PayoffEstimate:     payoff,
				LossEstimate:       loss,
				EV:                 ev,
				KellyFull:          kFull,
				KellyFractionUsed:  kUsed,
				RecommendedSizeUSD: size,
				Go:                 goN,
				Reason:             reason,
			}
			resMu.Lock()
			results = append(results, ar)
			resMu.Unlock()
		}()
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].Symbol < results[j].Symbol })

	notes := ""
	if btcKey == "" {
		notes += "No BTC series in series_json; benchmark_context vs BTC uses zeros. "
	}
	if ethKey == "" {
		notes += "No ETH series in series_json; benchmark_context vs ETH uses zeros. "
	}
	if req.TargetSymbol != "" {
		notes += "Filter or prioritize target_symbol in the assets list when interpreting results."
	}

	return &AnalyzeResponse{
		Benchmarks:        benchFactors,
		Assets:            results,
		DeployableBaseUSD: deploy,
		Notes:             strings.TrimSpace(notes),
	}, nil
}

func selectSymbols(req AnalyzeRequest, closesMap map[string][]float64, btcKey, ethKey string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		if _, ok := closesMap[s]; !ok {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range req.PortfolioSymbols {
		add(s)
	}
	if req.TargetSymbol != "" {
		add(req.TargetSymbol)
	}
	// Always include benchmarks if present
	if btcKey != "" {
		add(btcKey)
	}
	if ethKey != "" {
		add(ethKey)
	}
	if len(out) == 0 {
		for k := range closesMap {
			out = append(out, k)
		}
	}
	return out
}
