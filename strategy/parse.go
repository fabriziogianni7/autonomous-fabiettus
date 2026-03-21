package strategy

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// ExtractCloses parses Tokenaru/CoinGecko-like JSON into ascending-time close prices.
// Supports: top-level or nested { "prices": [[ts, price], ...] }, OHLC arrays, or { "closes": [n,n,...] }.
func ExtractCloses(raw []byte) ([]float64, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty json")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	// Unwrap Tokenaru envelope { ok, data, capability }
	if d, ok := top["data"]; ok && len(d) > 0 && d[0] == '{' {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(d, &inner); err == nil {
			if c, err2 := extractFromObject(inner); err2 == nil && len(c) >= 2 {
				return c, nil
			}
		}
		// data might be array (OHLC rows)
		if c, err2 := extractFromArrayJSON(d); err2 == nil && len(c) >= 2 {
			return c, nil
		}
	}
	if c, err := extractFromObject(top); err == nil && len(c) >= 2 {
		return c, nil
	}
	if c, err := extractFromArrayJSON(raw); err == nil && len(c) >= 2 {
		return c, nil
	}
	return nil, fmt.Errorf("could not extract price series (need prices[], OHLC array, or closes[])")
}

func extractFromObject(m map[string]json.RawMessage) ([]float64, error) {
	if raw, ok := m["closes"]; ok {
		var arr []float64
		if err := json.Unmarshal(raw, &arr); err == nil && len(arr) >= 2 {
			return arr, nil
		}
	}
	if raw, ok := m["prices"]; ok {
		return parsePricePairs(raw)
	}
	// nested market_chart
	for _, k := range []string{"market_chart", "market_data", "chart"} {
		if raw, ok := m[k]; ok {
			var inner map[string]json.RawMessage
			if err := json.Unmarshal(raw, &inner); err != nil {
				continue
			}
			if raw2, ok2 := inner["prices"]; ok2 {
				if c, err := parsePricePairs(raw2); err == nil && len(c) >= 2 {
					return c, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("no prices/closes in object")
}

func parsePricePairs(raw json.RawMessage) ([]float64, error) {
	var pairs [][]interface{}
	if err := json.Unmarshal(raw, &pairs); err != nil {
		return nil, err
	}
	out := make([]float64, 0, len(pairs))
	for _, row := range pairs {
		if len(row) < 2 {
			continue
		}
		p, ok := toFloat(row[1])
		if !ok || p <= 0 || math.IsNaN(p) {
			continue
		}
		out = append(out, p)
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("prices array too short")
	}
	return out, nil
}

func extractFromArrayJSON(raw []byte) ([]float64, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	if len(arr) < 2 {
		return nil, fmt.Errorf("array too short")
	}
	// Try [[t,o,h,l,c],...]
	first := string(arr[0])
	if len(first) > 0 && first[0] == '[' {
		var row []interface{}
		if err := json.Unmarshal(arr[0], &row); err == nil && len(row) >= 5 {
			out := make([]float64, 0, len(arr))
			for _, el := range arr {
				var r []interface{}
				if json.Unmarshal(el, &r) != nil || len(r) < 5 {
					continue
				}
				c, ok := toFloat(r[4])
				if ok && c > 0 && !math.IsNaN(c) {
					out = append(out, c)
				}
			}
			if len(out) >= 2 {
				return out, nil
			}
		}
	}
	// Try [{ "close": n }, ...]
	out := make([]float64, 0, len(arr))
	for _, el := range arr {
		var o struct {
			Close float64 `json:"close"`
		}
		if json.Unmarshal(el, &o) == nil && o.Close > 0 {
			out = append(out, o.Close)
			continue
		}
		var r []interface{}
		if json.Unmarshal(el, &r) == nil && len(r) > 0 {
			if v, ok := toFloat(r[len(r)-1]); ok && v > 0 {
				out = append(out, v)
			}
		}
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("could not parse OHLC array")
	}
	return out, nil
}

func toFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
