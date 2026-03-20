package tools

import (
	"strings"
	"testing"
)

func TestWalletGetPortfolio_NotConfigured(t *testing.T) {
	toolSet := NewTools("", nil)
	out, err := toolSet.ExecuteTool("wallet_get_portfolio", `{}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty message")
	}
	if !strings.Contains(out, "Alchemy") && !strings.Contains(out, "wallet") {
		t.Errorf("expected 'Alchemy' or 'wallet' in message, got: %s", out)
	}
}

func TestWalletGetPortfolioValue_NotConfigured(t *testing.T) {
	toolSet := NewTools("", nil)
	out, err := toolSet.ExecuteTool("wallet_get_portfolio_value", `{}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty message")
	}
}

func TestWalletGetActivity_NotConfigured(t *testing.T) {
	toolSet := NewTools("", nil)
	out, err := toolSet.ExecuteTool("wallet_get_activity", `{}`)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty message")
	}
}
