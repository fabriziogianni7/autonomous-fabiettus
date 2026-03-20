---
name: lifi
description: Cross-chain token swaps and bridges via LI.FI. Use the dedicated tools.
user-invocable: true
argument-hint: "[swap|bridge|track|routes] <details>"
---

# /lifi — Cross-Chain Swaps & Bridges

LI.FI is a cross-chain bridge and DEX aggregation protocol. It finds optimal routes across 35+ blockchains. **Use the dedicated tools** — do not build URLs or use http_request for LI.FI operations.

## Tools (use these, not http_request)

- **lifi_get_quote** — Get swap/bridge quote and transactionRequest. Required: from_chain, to_chain, from_token, to_token, from_amount. Omit from_address to use the configured wallet.
- **lifi_track_status** — Track cross-chain transfer. Required: tx_hash. Optional: from_chain, to_chain, bridge.
- **lifi_check_route** — Check if a route exists between chains and tokens. Required: from_chain, to_chain, from_token. Optional: to_token.
- **lifi_get_token** — Get token details (address, decimals, symbol). Required: chain, token (symbol or address).

## Arguments: `$ARGUMENTS`

Parse the arguments:
- **First positional arg**: Action — `swap`, `bridge`, `track`, `routes`, or a natural language request
- **Remaining args**: Details (token names, amounts, chains, tx hashes, etc.)

If no arguments are provided, ask the user what they want to do.

## Key Concepts

- **Native token address**: `0x0000000000000000000000000000000000000000` (for ETH, MATIC, BNB, etc.)
- **Amounts** are always in the token's **smallest unit** (wei): 1 ETH = 10^18, 1 USDC = 10^6
- Use **lifi_get_token** to resolve symbol → address and decimals before quoting
- The tools return `transactionRequest` objects. For wallet_execute_contract_call you MUST pass:
  - `to` — from transactionRequest.to
  - `data` — from transactionRequest.data (full hex string)
  - `value_wei` — convert transactionRequest.value from hex to decimal (0x0 → "0", 0x38D7EA4C68000 → "1000000000000000")
  - `chain_id` — from transactionRequest.chainId (e.g. 8453 for Base). **CRITICAL**: Wrong chain causes "FunctionDoesNotExist"

## Workflows (tool-based)

### Workflow 1 — Swap or Bridge Tokens

Use for any "swap X for Y" or "bridge tokens to chain" request.

1. **Identify parameters** from the user's request: source chain/token, destination chain/token, amount, wallet (or omit for agent wallet).

2. **Resolve token addresses** if the user gave symbols: call **lifi_get_token** with chain and token symbol. Extract `address` and `decimals` from the response.

3. **Convert amount to smallest unit**: human_amount × 10^decimals. Example: 1.5 ETH (18 decimals) → `1500000000000000000`.

4. **Get a quote**: call **lifi_get_quote** with from_chain, to_chain, from_token, to_token, from_amount. Omit from_address to use the configured wallet.

5. **Present summary to user** (REQUIRED before returning tx data):
   - Tokens and amounts, route/bridge used, estimated fees, estimated time, slippage setting

6. **If ERC20 token**: Check allowance. The spender is `transactionRequest.to` from the quote. If allowance < fromAmount, guide the user to approve first.

7. **Execute**: Call **wallet_execute_contract_call** with:
   - `to` = transactionRequest.to
   - `data` = transactionRequest.data (entire hex string, do not truncate)
   - `value_wei` = decimal string (convert hex: 0x0 → "0", 0xDE0B6B3A7640000 → "1000000000000000000")
   - `chain_id` = transactionRequest.chainId (e.g. 8453 for Base). **Required** — wrong chain causes FunctionDoesNotExist.
   - **Execute as soon as possible** after getting the quote. LI.FI quotes expire (solver orders are time-limited). If you ran quant or approval flow first, call **lifi_get_quote** again and use the fresh transactionRequest.

8. **If cross-chain**: Explain bridging is asynchronous. Offer to track with **lifi_track_status**.

### Workflow 2 — Track a Transfer

Use when the user wants to check status of a cross-chain transfer.

1. **Get tx hash** from the user (and optionally from_chain, to_chain).

2. **Check status**: call **lifi_track_status** with tx_hash.

3. **Interpret and report**:
   - `NOT_FOUND` — Not yet indexed. Wait a minute and retry.
   - `PENDING` — Transfer in progress. Poll every 30 seconds.
   - `DONE` + `COMPLETED` — Transfer complete. Report destination tx hash.
   - `DONE` + `PARTIAL` — User received bridged token but not final target. They may need to swap manually.
   - `DONE` + `REFUNDED` — Transfer failed, funds returned to source.
   - `FAILED` — Report error details, advise checking source chain tx on explorer.

### Workflow 3 — Check Route Before Quoting

Use when unsure if a route exists.

1. Call **lifi_check_route** with from_chain, to_chain, from_token (and optionally to_token).
2. If connections exist, proceed with **lifi_get_quote**. If not, suggest different tokens or chains.

### Workflow 4 — Discovery

- Token details (address, decimals) → **lifi_get_token**
- Route exists? → **lifi_check_route**
- For chains list, tokens list, or bridges/DEXes list: use **http_request** to `https://li.quest/v1/chains`, `/v1/tokens?chains={id}`, or `/v1/tools` if needed.

---

## Safety Protocol

These rules are **mandatory** — never skip them.

### Address Validation
- **ALWAYS** verify `fromAddress` is a valid hex address: starts with `0x`, 42 characters total, valid hex characters
- **NEVER** send to the zero address (`0x0000000000000000000000000000000000000000`) as `toAddress` — this burns funds permanently. The zero address is ONLY valid as a `fromToken`/`toToken` to represent native tokens.

### Amount Validation
- **ALWAYS** convert human-readable amounts to the token's smallest unit before calling lifi_get_quote
  - Check the token's `decimals` field (ETH=18, USDC=6, WBTC=8, DAI=18)
  - Formula: `amount_wei = human_amount × 10^decimals`
- Amounts must be positive integers (no decimals, no negatives, no zero)
- If the user says "1 ETH", send `1000000000000000000`, NOT `1`

### Slippage Validation
- Default to `0.03` (3%) if the user doesn't specify
- **WARN** the user if slippage > 3%
- **REJECT** slippage > 50% (`0.5`) — almost certainly an error
- Slippage must be between 0 and 1 (0% to 100%)

### ERC20 Token Approvals
- **ALWAYS** check token allowance before executing ERC20 swaps
- If allowance < fromAmount, guide the user to approve the token first
- **NEVER** approve unlimited amounts unless the user explicitly requests it
- The spender address is `transactionRequest.to` from the quote response

### Transaction Presentation
- **ALWAYS** present a human-readable summary before returning any `transactionRequest` data
- **NEVER** return raw transaction data without explanation

### Cross-Chain Safety
- Explain that cross-chain bridges are **non-atomic** — funds may be in transit for minutes to hours
- After submitting a source chain tx, always offer to track with lifi_track_status
- If a transfer shows `PARTIAL` status, explain the user received bridged tokens but not the final target token

### No Route Found
- If lifi_get_quote returns no route: use **lifi_check_route** to verify the pair. Suggest different tokens, smaller amount, or alternative chains.

### Quote Freshness
- **LI.FI quotes expire** (solver orders are time-limited). If you ran quant or approval flow *before* executing, call **lifi_get_quote** again immediately before `wallet_execute_contract_call` and use the fresh `transactionRequest`. Stale quotes cause `FunctionDoesNotExist` reverts.

---

## Error Handling

| Error | Cause | Action |
|-------|-------|--------|
| HTTP 429 | Rate limited | Wait 30s, retry with exponential backoff. |
| No route found | Route unsupported or amount too small/large | Use lifi_check_route. Suggest different tokens, smaller amount, or alternative chains. |
| Slippage error | Price moved beyond tolerance | Increase slippage (e.g. 0.03 → 0.05) and retry. Warn user. |
| Insufficient balance | Wallet lacks funds | Report shortfall. Check balance with wallet tools. |
| Transaction reverted | Price moved, liquidity drained, or gas too low | Explain causes. Suggest retrying with higher slippage or gas. |
| PARTIAL completion | Bridge delivered token but not final swap | User has bridged asset on destination. They can swap manually or retry. |
| REFUNDED | Bridge failed, funds returned | Confirm refund on source chain. Suggest different bridge. |
| Invalid address | Malformed hex address | Verify: `0x` prefix, 42 characters, valid hex. |
| FunctionDoesNotExist | Wrong chain_id, truncated/malformed data, or **stale quote** | Pass chain_id from transactionRequest.chainId. Ensure data is complete (no truncation). Convert value from hex to decimal. **CRITICAL**: Execute immediately after lifi_get_quote—quotes expire (solvers have time-limited orders). If you waited for approval or ran other tools first, fetch a **fresh quote** right before wallet_execute_contract_call. |

---

## Chain Quick Reference

| Chain | ID | Native Token |
|-------|----|-------------|
| Ethereum | 1 | ETH |
| Polygon | 137 | MATIC |
| Arbitrum One | 42161 | ETH |
| Optimism | 10 | ETH |
| BSC | 56 | BNB |
| Base | 8453 | ETH |
| Avalanche | 43114 | AVAX |
| Fantom | 250 | FTM |
| zkSync Era | 324 | ETH |
| Gnosis | 100 | xDAI |
| Scroll | 534352 | ETH |
| Linea | 59144 | ETH |
| Blast | 81457 | ETH |
| Mode | 34443 | ETH |

Native token address on all EVM chains: `0x0000000000000000000000000000000000000000`
