---
name: onchain-trading
description: AI-discretionary onchain trading workflow. Use when executing trades: gather market data via Tokenaru, form a thesis, then execute via wallet tools. Records outcomes for PnL awareness.
---

# Onchain Trading MVP

## Workflow

1. **Gather context**: Use `http_request` with Tokenaru (`https://tokenaru.vercel.app/api/lookup?q=<query>`) for:
   - Current prices, addresses, trending tokens
   - Historical price data (e.g. `bitcoin price history last 30 days`) for assets being evaluated
   - OHLC data (e.g. `ETH OHLC 7 days`, `SOL OHLC 30 days`) for momentum and trend analysis. Valid days: 1, 7, 14, 30, 90, 180, 365.
2. **Check balance**: Use `wallet_get_balance` before committing capital.
3. **Form thesis**: Decide what to buy/sell and why, based on current prices, historical context, and OHLC.
4. **Execute**: Use `wallet_execute_transfer` for native token sends, or `wallet_execute_contract_call` for DEX swaps or contract interactions. Include `to`, `value_wei`, and `data` as needed.
5. **Record**: Use `wallet_list_transactions` to review outcomes and reason about PnL and runway.

## Notes

- Start small. Prefer contract calls for swaps (Uniswap, etc.) when you have the ABI and calldata.
- Approvals may be required for large transfers; the user replies `approve: tx_<id>`.
- Keep runway in mind: reserve enough for inference and data costs.
