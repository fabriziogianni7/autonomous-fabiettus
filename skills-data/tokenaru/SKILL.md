---
name: tokenaru
description: Fetch onchain data (prices, addresses, trending coins) via Tokenaru. Use when the user or agent needs token prices, contract addresses, trending tokens, or other onchain lookup data.
---

# Tokenaru Onchain Lookup

## When to use

Use `http_request` with Tokenaru when you need:
- Token prices
- Contract addresses
- Trending coins or tokens
- Other onchain lookup data
- historical prices
- OHLC data


# Agent Integration Guide

Tokenaru turns natural language into CoinGecko data. One HTTP call, one query. Payment required (x402).

## Endpoint

```
GET /api/lookup?q=<query>
```

Optional: `&limit=<n>` for trending/markets (e.g. `limit=5`).

## Payment

The endpoint returns **402 Payment Required**. Use an x402-aware client (e.g. `@x402/fetch`) to pay and retry.

Default price: $0.001 per request (configurable via `X402_PRICE`).

## Constraints

| Constraint | Limit | Notes |
|------------|-------|-------|
| **Historical range** | 365 days max | Public API. "All time" / "max" requests are capped at 365 days. |
| **Historical date** | Past 365 days only | `coin_history` (single date) requires dates within the last 365 days. Future dates or dates older than 365 days return 401. |
| **OHLC days** | 1, 7, 14, 30, 90, 180, 365 | Only these values are supported. |
| **Plan steps** | 1–3 | Multi-step plans (e.g. address → price) limited to 3 steps. |

## Query Examples by Use Case

### Address lookup (simple)

| Query | Returns |
|-------|---------|
| USDC on Ethereum mainnet | Contract address |
| WETH on Base | Contract address |
| USDT on Polygon | Contract address |
| DAI on Arbitrum | Contract address |

### Price

| Query | Returns |
|-------|---------|
| price of bitcoin | Price data |
| price of ethereum | Price data |
| price of solana | Price data |
| SOL price | Price data |

### Markets

| Query | Returns |
|-------|---------|
| top 10 coins by market cap | Ranked market data |
| top 20 coins | Ranked market data |
| coins markets usd | Market data in USD |

### Coin data

| Query | Returns |
|-------|---------|
| bitcoin metadata | Full coin metadata (slim) |
| ethereum coin data | Full coin metadata |
| full data for solana | Full coin metadata |

### Historical (single date)

| Query | Returns |
|-------|---------|
| bitcoin price on 30-12-2023 | Historical snapshot for that date |
| ETH price on 01-01-2024 | Historical snapshot |
| bitcoin price on 12 March 2025 | Historical snapshot (date format flexible) |

**Date format:** Use `dd-mm-yyyy` or natural language (e.g. "12 March 2025"). Must be within the last 365 days.

### Historical (time series)

| Query | Returns |
|-------|---------|
| bitcoin price history last 30 days | Price/mcap/volume time series |
| ethereum market chart last 7 days | Time series |
| SOL price history max | Time series (capped at 365 days) |

**Constraint:** 1–365 days. "Max" or "all time" is capped at 365 days.

### OHLC

| Query | Returns |
|-------|---------|
| bitcoin OHLC last week | OHLC candlestick data |
| ETH OHLC 7 days | OHLC data |
| solana candlestick data 30 days | OHLC data |

**Constraint:** Valid days: 1, 7, 14, 30, 90, 180, 365.

### Trending

| Query | Returns |
|-------|---------|
| trending coins | Trending coins (coins only) |
| trending tokens | Trending coins |
| what's trending | Trending coins |

### Supported currencies

| Query | Returns |
|-------|---------|
| supported vs currencies | List of supported fiat/crypto currencies |
| list of supported currencies | Same |

### Multi-step (address → price)

| Query | Returns |
|-------|---------|
| price of USDC on Ethereum | Resolves address, then fetches price |
| USDC on Base price | Same |

## Response Shapes

**Address lookup success:**
```json
{ "ok": true, "address", "symbol", "name", "chainId", "chainName", "coingeckoPlatformId", "source" }
```

**Other capabilities:**
```json
{ "ok": true, "capability", "data", "summary?" }
```

**Error:**
```json
{ "ok": false, "error", "message" }
```
or
```json
{ "ok": false, "capability", "error", "message" }
```

Successful responses may include a `summary` field (1–2 sentence LLM summary) when `OPENAI_API_KEY` is set.

## How to Use

1. Build the URL: `baseUrl + "/api/lookup?q=" + encodeURIComponent(userQuery)`
2. If you have x402 payment capability, use `wrapFetchWithPayment` or equivalent to pay and fetch
3. Parse the JSON response
4. On 402, the response body describes the payment required; retry with payment headers

## Why Tokenaru Instead of Calling CoinGecko Directly

- One query, one response. No need to reason about endpoints or params.
- Server holds API keys; you pay per request.
- Built-in chaining (e.g. address → price) without orchestrating multiple calls.
- Cached address lookups reduce latency.

## Agent behavior: multifactor tool

After fetching historical/OHLC responses, pass the JSON bodies into **`strategy_factor_analysis`** as `series_json` (map key → JSON). Include **bitcoin** and **ethereum** keys for benchmark-relative factors. See STRATEGY.md.

## Agent behavior: when to fetch historical and OHLC

Before evaluating trades, base analysis on data—not just spot prices:

- **Momentum / speculative trades**: Fetch OHLC and price history. Examples: `bitcoin OHLC 7 days`, `ETH price history last 30 days`, `SOL OHLC 30 days`, `[trending token] OHLC 14 days`.
- **Blue-chip DCA / bootstrap**: Historical context optional but helpful. Example: `bitcoin price history last 30 days`.
- **Single-date context**: `bitcoin price on 01-01-2025` (dates must be within last 365 days).
