package strategy

import "math"

// DeriveProbPayoffLoss maps signal and volatility into EV/Kelly inputs (long bias).
func DeriveProbPayoffLoss(signal float64, vol14d float64) (p, payoff, loss float64) {
	p = 0.5 + 0.12*signal
	p = math.Max(0.45, math.Min(0.62, p))
	payoff = 0.04 + 0.25*math.Max(0, signal)
	payoff = math.Min(0.18, math.Max(0.02, payoff))
	loss = 0.025 + 2.0*math.Max(0.01, vol14d)
	loss = math.Min(0.14, math.Max(0.02, loss))
	return p, payoff, loss
}

// ExpectedValue EV = p*payoff - (1-p)*loss
func ExpectedValue(p, payoff, loss float64) float64 {
	return p*payoff - (1-p)*loss
}

// KellyFraction full Kelly f* = (p*b - q)/b, b = payoff/loss, q = 1-p
func KellyFraction(p, payoff, loss float64) float64 {
	if loss <= 0 || payoff <= 0 {
		return 0
	}
	b := payoff / loss
	q := 1 - p
	num := p*b - q
	if num <= 0 {
		return 0
	}
	return num / b
}

// ApplyKellyFraction applies half-Kelly (bluechip) or quarter-Kelly (speculative).
func ApplyKellyFraction(fullKelly float64, speculative bool) float64 {
	if speculative {
		return fullKelly / 4
	}
	return fullKelly / 2
}

// CapSizeUSD applies STRATEGY-style caps.
func CapSizeUSD(kellyFrac, deployableBase, portfolioValue float64, speculative bool) float64 {
	maxPos := 0.15 * portfolioValue
	if speculative {
		maxPos = 0.05 * portfolioValue
	}
	maxScan := 0.20 * deployableBase
	raw := kellyFrac * deployableBase
	s := raw
	if s > maxPos {
		s = maxPos
	}
	if s > maxScan {
		s = maxScan
	}
	if s < 0 {
		s = 0
	}
	return s
}
