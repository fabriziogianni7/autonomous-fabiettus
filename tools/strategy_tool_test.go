package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStrategyFactorAnalysisTool(t *testing.T) {
	ts := NewTools("", nil)
	mkSeries := func(n int, start, step float64) map[string]interface{} {
		prices := make([][]float64, n)
		p := start
		for i := 0; i < n; i++ {
			prices[i] = []float64{float64(i * 86400000), p}
			p += step
		}
		return map[string]interface{}{"prices": prices}
	}
	series := map[string]interface{}{
		"bitcoin":  mkSeries(45, 100, 0.15),
		"ethereum": mkSeries(45, 50, 0.08),
		"SOL":      mkSeries(45, 20, 0.12),
	}
	args := map[string]interface{}{
		"series_json":                       series,
		"portfolio_symbols":                 []interface{}{"SOL"},
		"target_symbol":                     "SOL",
		"portfolio_value_usd":               10000.0,
		"deployable_usdc_above_reserve_usd": 500.0,
		"trade_type":                        "buy_with_usdc",
		"risk_tier":                         "speculative",
	}
	b, _ := json.Marshal(args)
	out, err := ts.ExecuteTool("strategy_factor_analysis", string(b))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(out, "Error:") {
		t.Fatal(out)
	}
	var resp struct {
		DeployableBaseUSD float64 `json:"deployable_base_usd"`
		Assets            []struct {
			Symbol string `json:"symbol"`
			Go     bool   `json:"go"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if resp.DeployableBaseUSD != 500 {
		t.Fatalf("deployable got %v", resp.DeployableBaseUSD)
	}
	if len(resp.Assets) == 0 {
		t.Fatal("no assets")
	}
}

func TestParseSeriesJSONField_String(t *testing.T) {
	s := `{"a":{"prices":[[1,10],[2,11]]}}`
	m, err := parseSeriesJSONField(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Fatalf("%v", m)
	}
}
