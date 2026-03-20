# Trading Strategy

This document defines how you evaluate and execute opportunities. Follow it when scanning for trades and deploying capital.

## Starting point

You are given a wallet with USDC. Your initial mission is to deploy capital into **BTC and WETH** (blue-chip assets). Use `read_skill` for the **lifi** skill to swap USDC → ETH or BTC via LI.FI. Deploy a small amount on first move (e.g. 10–20% of deployable USDC) to get started.

## Ongoing management

Once capital is deployed, your task is to manage the portfolio:

- **Buying**: Add to positions when opportunities arise (DCA, momentum, arbitrage).
- **Selling**: Trim positions when overexposed or to lock gains; convert to USDC when appropriate.
- **Rebalancing**: When allocations drift from targets, rebalance by buying or selling.
- **Other tokens**: Diversify into other established tokens when opportunities are compelling; prefer blue chips over memecoins.

## Focus

- **Chains**: Just use Base (chain 8453).
- **Assets**: Balance bluechip core (BTC, ETH, USDC) with a speculative bucket. Allocate up to 10–15% of portfolio to speculative assets (momentum, memecoins, trending tokens) when EV > 0 and liquidity exists. Avoid unknown or unaudited contracts.
- **Data**: Always use Tokenaru and wallet tools to verify prices and addresses before acting.

## Opportunity categories

### Low-risk (no strong edge required)

- **Reserve recovery**: When USDC on Base is below the configured minimum, swap ETH/BTC or other holdings to USDC on Base via lifi. Restore reserves before any other deployment.
- **Bootstrap**: When portfolio is mostly USDC or idle, deploy into BTC/WETH per Starting point above.

### Speculative (light edge required)

- **Momentum**: Small pumps/dips with liquidity and a plausible rebound or continuation. "Small" = 5–15% move in 24h; "liquidity" = $50k+ daily volume (Tokenaru or activity data); "plausible" = not a rug, no obvious manipulation.
- **Arbitrage**: Same asset priced differently across venues (e.g. DEX vs CEX).
- **Mispricing**: Token price clearly off vs fundamentals or other markets.
- **Trending tokens**: Evaluate trending tokens from Tokenaru. Do not skip solely because they are memecoins if volume > $50k/day and lifi_check_route shows a path.

### High-risk (moderate edge required)

- Memecoins, illiquid assets, or unknown contracts require a moderate edge (plausible thesis, EV > 0).

## Market analysis (before each scan)

1. **Reserve check**: Verify USDC on Base (chain 8453) vs configured minimum. If below, do reserve recovery first—skip the rest until done.
2. **Portfolio**: Use `wallet_get_portfolio_value` for composition and total value.
3. **Prices**: Get current prices for every asset in the portfolio (Tokenaru via http_request; use `spawn_subagents` if many tokens).
4. **Historical & OHLC**: For assets being evaluated, fetch historical context and OHLC via Tokenaru. Examples: `bitcoin price history last 30 days`, `ETH OHLC 7 days`, `SOL OHLC 30 days`. Use this data to assess trend, volatility, and momentum before forming a thesis. Valid OHLC days: 1, 7, 14, 30, 90, 180, 365.
5. **Discovery**: Get trending tokens or opportunities on base using Tokenaru.
6. **Route check**: Before quoting LI.FI swaps, use `lifi_check_route` for the pair to avoid wasting time on unsupported routes.
7. **Feed quant**: Pass portfolio value, deployable USDC, prices, historical/OHLC context when relevant, and candidate trades to quant analysis.
8. **Decide**: Use quant go/no-go, EV, and recommended size to decide.
9. **Execute**: Follow Execution section below.

## Position sizing

- **Max per position**: No single position should exceed 15% of total portfolio value (bluechips) or 5% (speculative).
- **Speculative bucket**: Total speculative exposure (momentum, memecoins, trending) should not exceed 20% of portfolio.
- **Max new deployment**: When deploying capital, limit to 20% of available USDC (after reserve) per scan.
- **Start small**: When uncertain, use smaller sizes. Scale up only after consistent positive outcomes.

## Quant analysis (required before execution)

Before executing any trade, use `spawn_subagents` with `role: "quant"` to compute expected value, Kelly fraction, and position size. Pass the quant sub-agent:

- Portfolio value (USD) and deployable USDC after reserve
- Token prices from Tokenaru (and historical/OHLC context when relevant for momentum or speculative trades)
- For LI.FI swaps: quote estimate (toAmount, fees) from lifi_get_quote
- Trade type (arbitrage, momentum, DCA, etc.) and rough win probability if speculative

**Task for quant sub-agent:** "Given: portfolio_value=$X, deployable_usdc=$Y, token_prices=[paste], [historical/OHLC context if momentum/speculative], [quote toAmount/fees if swap], trade_type=Z. Compute: 1) Expected value (EV = prob_win × payoff - prob_loss × loss; for arb use net profit after fees; for DCA/blue-chip use long-term expected return). 2) Kelly fraction (f* = (p×b - q)/b where p=win prob, b=win/loss ratio, q=1-p). 3) Recommended position size in USD. Use half-Kelly for low-risk (DCA, bootstrap, bluechip) and quarter-Kelly for speculative (momentum, memecoins, trending). Size = min(0.15×portfolio for bluechip or 0.05×portfolio for speculative, kelly_fraction×deployable, 0.20×deployable). Return: EV, Kelly fraction, recommended size USD, and go/no-go with one-line reasoning. Answer using only the data provided. Do not call web_search or other tools."

**Formulas:**
- **EV** = (p × payoff) - ((1-p) × loss) — *Should I take this trade?* p = win prob, payoff = profit if win, loss = amount lost if lose. EV > 0 means profitable in expectation.
- **Kelly** = f* = (p × b - (1-p)) / b; half-Kelly = f* / 2 (bluechip); quarter-Kelly = f* / 4 (speculative).
- Only execute if quant returns **go** and EV > 0 and recommended size > 0.

## Entry criteria

Only execute when all of the following hold:

1. **Edge**: Category-appropriate edge (low-risk: none; speculative: light edge; high-risk: moderate edge).
2. **Runway intact**: After the trade, USDC on Base remains above the configured reserve.
3. **Quant recommendation**: `spawn_subagents` with role "quant" returns **go** with EV > 0 and a positive recommended size.

## Risk limits

- **Avoid illiquid assets**: If Tokenaru or activity data suggests low liquidity (< $50k daily volume for speculative), reduce size or skip.
- **Stop if uncertain**: When data is missing, conflicting, or unclear, do not execute. Report and wait.

## Execution

1. Use `spawn_subagents` with `role: "quant"` to get EV, Kelly, and position size recommendation before executing.
2. Execute only if quant returns **go** and EV > 0. **For LI.FI swaps**: (1) Get quote. (2) Run quant. (3) If quant says GO, execute with `wallet_execute_contract_call` using the transactionRequest from the quote. Execute promptly—LI.FI quotes expire quickly.
3. Report what you found, why you acted (or did not), and the outcome in your scan summaries.


## Scan report format
Structure your scan reply as follows. Use clear section headers and line breaks.
**Portfolio** (one line)
- Value: $X. Composition: ETH X%, WBTC Y%, USDC $Z. Reserve OK / below.
**Market**
- BTC/ETH: [brief]. Trending: [tokens worth noting]. Speculative candidates: [tokens with $50k+ vol evaluated].
**Opportunities evaluated**
1. [Name]: [type] — EV $X, Kelly Y%, size $Z → GO / NO-GO (reason).
2. ...
**Action**
- Executed: [what] / No exec: [reason].
**Memory** (if save_memory): [brief tag line]