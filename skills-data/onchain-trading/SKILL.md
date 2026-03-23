---
name: onchain-trading
description: AI-discretionary onchain trading workflow. Use when executing trades: gather market data via Tokenaru, run multifactor analysis, then execute via LI.FI + wallet tools. Records outcomes for PnL awareness.
---

# Onchain Trading MVP

## Workflow

1. **Gather context**: Use `http_request` with Tokenaru (`https://tokenaru.vercel.app/api/lookup?q=<query>`) for:
   - Current prices, addresses, trending tokens
   - Historical price data (e.g. `bitcoin price history last 30 days`) for assets being evaluated
   - OHLC data (e.g. `ETH OHLC 7 days`, `SOL OHLC 30 days`) for momentum and trend analysis. Valid days: 1, 7, 14, 30, 90, 180, 365.
   - **Always** fetch **bitcoin** and **ethereum** benchmark series in addition to portfolio/candidate assets (for relative strength).
2. **Portfolio**: Use `wallet_get_portfolio_value` (and `wallet_get_balance` if needed) for holdings, USD values, and **which asset funds the trade** (USDC above reserve vs ETH/WBTC as `from_token`).
3. **Multifactor**: Call **`strategy_factor_analysis`** with a `series_json` object mapping each fetch key (e.g. `bitcoin`, `ethereum`, `SOL`) to that response’s JSON body. **Never call without first completing step 1**—the tool requires non-empty OHLC series and will fail otherwise. Set `trade_type` (`buy_with_usdc`, `swap_asset`, `rebalance_asset`, …), `risk_tier`, and deployable fields per STRATEGY.md.
4. **Quant**: Run `spawn_subagents` with `role: quant` (or inline if disabled) using factor output + any `lifi_get_quote` estimate.
5. **Execute swaps**: Prefer **LI.FI** for token swaps on Base—read the **lifi** skill. Flow: `lifi_get_token` → `lifi_check_route` → `lifi_get_quote` → (analysis) → **refresh `lifi_get_quote`** if delayed → `wallet_execute_contract_call` with the fresh `transactionRequest`. Use **any held asset** as `from_token`, not only USDC.
6. **Native sends**: Use `wallet_execute_transfer` only for simple native token sends (not DEX swaps).
7. **Record**: Use `wallet_list_transactions` to review outcomes and reason about PnL and runway.

## Notes

- Start small. Keep Base **USDC reserve** for inference; see STRATEGY.md and PERSONALITY.md.
- Approvals may be required for large transfers; the user replies `approve: tx_<id>`.
- LI.FI quotes expire—always re-quote immediately before execution if other tools ran in between.
