package strategy

import (
	"encoding/json"
	"testing"
)

func TestExtractCloses_PricesArray(t *testing.T) {
	raw := []byte(`{"prices":[[1,100],[2,102],[3,101],[4,105]]}`)
	c, err := ExtractCloses(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 4 || c[3] != 105 {
		t.Fatalf("got %v", c)
	}
}

func TestExtractCloses_TokenaruEnvelope(t *testing.T) {
	inner := map[string]interface{}{
		"prices": [][]float64{{1, 10}, {2, 11}, {3, 12}, {4, 13}},
	}
	data, _ := json.Marshal(inner)
	raw, _ := json.Marshal(map[string]json.RawMessage{"data": data})
	c, err := ExtractCloses(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 4 || c[len(c)-1] != 13 {
		t.Fatalf("got %v", c)
	}
}

func TestExtractCloses_ClosesField(t *testing.T) {
	raw := []byte(`{"closes":[1.0,1.1,1.2,1.15,1.25]}`)
	c, err := ExtractCloses(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 5 {
		t.Fatalf("got %v", c)
	}
}

func TestAnalyzeConcurrent(t *testing.T) {
	// Synthetic uptrend series
	mk := func(n int, start, step float64) []byte {
		arr := make([][]float64, n)
		p := start
		for i := 0; i < n; i++ {
			arr[i] = []float64{float64(i), p}
			p += step
		}
		b, _ := json.Marshal(map[string]interface{}{"prices": arr})
		return b
	}
	req := AnalyzeRequest{
		SeriesJSON: map[string]json.RawMessage{
			"bitcoin":  json.RawMessage(mk(40, 100, 0.2)),
			"ethereum": json.RawMessage(mk(40, 50, 0.1)),
			"SOL":      json.RawMessage(mk(40, 20, 0.15)),
		},
		PortfolioSymbols:              []string{"SOL"},
		PortfolioValueUSD:             10000,
		DeployableUSDCAboveReserveUSD: 1000,
		TradeType:                     "buy_with_usdc",
		RiskTier:                      "speculative",
	}
	resp, err := Analyze(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Assets) < 1 {
		t.Fatalf("expected assets, got %#v", resp)
	}
	if resp.DeployableBaseUSD != 1000 {
		t.Fatalf("deployable %v", resp.DeployableBaseUSD)
	}
}
