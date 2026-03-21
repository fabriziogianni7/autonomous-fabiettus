package strategy

import (
	"math"
)

// Factors holds computed signals from a close-price series (oldest → newest).
type Factors struct {
	Momentum7d   float64 `json:"momentum_7d"`
	Momentum14d  float64 `json:"momentum_14d"`
	Momentum30d  float64 `json:"momentum_30d"`
	Vol14d       float64 `json:"vol_14d"` // stdev of daily log returns, annualized-ish scale ~ daily*sqrt(252) not applied; raw daily stdev
	Mean30d      float64 `json:"mean_30d"`
	ZScore30d    float64 `json:"zscore_30d"`
	RangePos30d  float64 `json:"range_pos_30d"` // 0–1 position in 30d high-low
	SamplePoints int     `json:"sample_points"`
}

// BenchmarkContext compares asset momentum to BTC/ETH (same windows).
type BenchmarkContext struct {
	VsBTCMom7d  float64 `json:"vs_btc_mom_7d"`
	VsETHMom7d  float64 `json:"vs_eth_mom_7d"`
	VsBTCMom14d float64 `json:"vs_btc_mom_14d"`
	VsETHMom14d float64 `json:"vs_eth_mom_14d"`
}

// ComputeFactors derives multifactor metrics from closes (ascending time).
func ComputeFactors(closes []float64) Factors {
	var f Factors
	n := len(closes)
	f.SamplePoints = n
	if n < 3 {
		return f
	}
	last := closes[n-1]
	f.Momentum7d = momentumAt(closes, 7)
	f.Momentum14d = momentumAt(closes, 14)
	f.Momentum30d = momentumAt(closes, 30)
	f.Vol14d = dailyReturnStdev(closes, 14)
	win := min(30, n)
	sum := 0.0
	for i := n - win; i < n; i++ {
		sum += closes[i]
	}
	f.Mean30d = sum / float64(win)
	std := sampleStd(closes[n-win:], f.Mean30d)
	if std > 1e-12 {
		f.ZScore30d = (last - f.Mean30d) / std
	}
	hi, lo := closes[n-win], closes[n-win]
	for i := n - win; i < n; i++ {
		if closes[i] > hi {
			hi = closes[i]
		}
		if closes[i] < lo {
			lo = closes[i]
		}
	}
	if hi > lo {
		f.RangePos30d = (last - lo) / (hi - lo)
	} else {
		f.RangePos30d = 0.5
	}
	return f
}

func momentumAt(closes []float64, days int) float64 {
	n := len(closes)
	if n < 2 || days < 1 {
		return 0
	}
	idx := n - 1 - days
	if idx < 0 {
		idx = 0
	}
	old := closes[idx]
	last := closes[n-1]
	if old <= 0 {
		return 0
	}
	return (last / old) - 1
}

func dailyReturnStdev(closes []float64, maxDays int) float64 {
	n := len(closes)
	if n < 3 {
		return 0
	}
	start := n - 1 - maxDays
	if start < 0 {
		start = 0
	}
	var rets []float64
	for i := start + 1; i < n; i++ {
		if closes[i-1] <= 0 {
			continue
		}
		r := math.Log(closes[i] / closes[i-1])
		rets = append(rets, r)
	}
	if len(rets) < 2 {
		return 0
	}
	m := mean(rets)
	return sampleStd(rets, m)
}

func mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func sampleStd(xs []float64, mu float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		d := x - mu
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)-1))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// BenchmarkCompare subtracts benchmark momentum from asset momentum.
func BenchmarkContextFor(assetMom7, assetMom14, btcMom7, btcMom14, ethMom7, ethMom14 float64) BenchmarkContext {
	return BenchmarkContext{
		VsBTCMom7d:  assetMom7 - btcMom7,
		VsETHMom7d:  assetMom7 - ethMom7,
		VsBTCMom14d: assetMom14 - btcMom14,
		VsETHMom14d: assetMom14 - ethMom14,
	}
}

// SignalScore maps factors and benchmark context to [-1, 1] (bullish positive).
func SignalScore(f Factors, bc BenchmarkContext, isBenchmark bool) float64 {
	s := 0.0
	s += 2.0 * f.Momentum14d
	s += 1.0 * f.Momentum7d
	if !isBenchmark {
		s += 0.8 * bc.VsBTCMom14d
		s += 0.6 * bc.VsETHMom14d
	}
	// Mean reversion tilt when stretched low
	if f.ZScore30d < -1.0 {
		s += 0.15 * (-f.ZScore30d)
	} else if f.ZScore30d > 1.5 {
		s -= 0.1 * f.ZScore30d
	}
	// Range: high in band = caution
	if f.RangePos30d > 0.92 {
		s -= 0.08
	} else if f.RangePos30d < 0.08 {
		s += 0.05
	}
	// Vol penalty
	s -= 1.2 * math.Min(f.Vol14d, 0.15)
	return math.Max(-1, math.Min(1, math.Tanh(s)))
}
