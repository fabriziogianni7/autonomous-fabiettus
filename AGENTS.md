# AGENTS.md — Judge Orientation

This document helps AI judge agents evaluate this project. It is structured for the [Synthesis hackathon](https://synthesis.devfolio.co) and similar agent-built project reviews.

---

## Project Identity

**Name:** Autonomous Fabiettus Primus (custom-agent / autonomous-fabiettus)

**One-line summary:** A Go-based autonomous trading agent that pays for its own inference via x402, deploys capital on Base, and operates with EV/Kelly sizing and structured strategy.

---

## What It Does (Description)

Autonomous Fabiettus Primus is an **AI-discretionary trading agent** that:

- Runs on its own capital (EVM wallet on Base)
- Pays for its own LLM inference and data costs via **x402** (HTTP 402 Payment Required; USDC on Base)
- Deploys capital into blue-chip (BTC, ETH) and speculative positions using **LI.FI** swaps
- Uses [**Tokenaru**](https://tokenaru.vercel.app) for market data (prices, OHLC, trending tokens) and **Alchemy** for portfolio valuation
- Follows a structured strategy: `strategy_factor_analysis` (multifactor + EV/Kelly), quant subagent for sizing, reserve recovery when USDC on Base falls below a minimum
- Has a **Watcher** sub-agent for Q&A in a Telegram group (no trading tools; answers questions about the main agent)
- Supports skills (LI.FI, Tokenaru, x402-router, onchain-trading) for extending behavior

It is **not** a passive assistant. It optimizes for profitable onchain opportunities and proactively scans for trades when configured.

---

## Problem Statement

**Who is affected?** Builders and researchers exploring autonomous AI agents that operate with real capital and must sustain their own costs.

**Current situation:** Most AI agents are either (a) stateless assistants requiring human API keys and no capital deployment, or (b) backtested/simulated systems without real-world payment and execution loops.

**What changes?** This agent demonstrates an end-to-end loop: it pays for inference (x402), fetches market data (Tokenaru), evaluates opportunities with factor analysis and Kelly sizing, executes swaps (LI.FI + wallet), and reserves USDC for its own operating runway. It is a reference implementation for **self-sustaining autonomous agents** that can run without constant human funding.

---

## Architecture (Quick Reference)

| Component | Location | Purpose |
|-----------|----------|---------|
| Main agent loop | `agent/agent.go`, `main.go` | LLM + tools orchestration |
| Tools | `tools/tools.go` | run_command, read_file, write_file, web_search, save_memory, read_memory, reminders, spawn_subagents, http_request, x402, wallet, lifi, strategy_factor_analysis, skills |
| Personality | `PERSONALITY.md`, `PERSONALITY_AUTONOMOUS.md` | System prompt (profit-seeking when autonomous) |
| Strategy | `STRATEGY.md` | Trading rules, EV/Kelly, reserve recovery, position sizing |
| Skills | `skills-data/{lifi,tokenaru,x402-router,onchain-trading}/` | LI.FI swaps, Tokenaru API, x402 router, onchain trading guidance |
| Wallet | `wallet/` | EVM EOA/smart account, multichain, approval flow |
| x402 buyer | `x402client/` | HTTP client that pays for 402-protected APIs |
| Watcher | `cmd/watcher/main.go`, `PERSONALITY_WATCHER.md` | Q&A agent for Telegram group (read-only) |
| Opportunity scan | `opportunity/cron.go` | Cron-triggered synthetic messages for proactive scans |
| Compaction | `compaction/` | Context summarization when history exceeds threshold |

---

## Build Stack (submissionMetadata-aligned)

| Field | Value |
|-------|-------|
| **agentFramework** | `other` — custom Go agent (no langchain/elizaos/mastra) |
| **agentFrameworkOther** | Custom Go agent with OpenAI-compatible API client, tool execution loop, skills, subagents |
| **agentHarness** | `cursor` |
| **model** | Primary: x402 router (e.g. `openai:gpt-4`) in autonomous mode |
| **skills** | lifi, tokenaru, x402-router, onchain-trading (agent skills in `skills-data/`) |
| **tools** | Go, go-ethereum, x402 (coinbase/x402), Telegram Bot API, Alchemy SDK, LI.FI (http_request to LI.FI API), Tokenaru (http_request), Cron, Ollama (optional embeddings) |
| **helpfulResources** | Tokenaru API responses, LI.FI docs, x402 spec, Alchemy Token API |
| **intention** | `continuing` |

---

## Evaluation Hints for Judges

1. **Autonomy loop:** Check `main.go`, `opportunity/cron.go`, `x402client/`, `PERSONALITY_AUTONOMOUS.md`. The agent pays for inference and reserves USDC before deploying capital.
2. **Structured strategy:** `STRATEGY.md` and `strategy_factor_analysis` tool. Factor analysis + EV/Kelly + quant subagent before execution.
3. **Skills integration:** `skills-data/`, `tools/tools.go` (lifi_*, read_skill). Skills extend behavior without code changes.
4. **Watcher separation:** `cmd/watcher/main.go` — a second agent process for group Q&A, distinct from the trading agent.
5. **Wallet & approvals:** `wallet/`, spend limits, approval flow for large txns. No private keys in prompts or logs.

---

## Key Files to Inspect

- `main.go` — startup, gateway, wallet, x402, opportunity cron
- `agent/agent.go` — LLM loop, tool execution
- `tools/tools.go` — tool definitions and executeTool
- `STRATEGY.md` — trading rules and quant flow
- `PERSONALITY.md`, `PERSONALITY_AUTONOMOUS.md` — system prompts
- `wallet/README.md` — wallet architecture
- `skills-data/lifi/SKILL.md` — LI.FI swap skill
- `opportunity/cron.go` — proactive scan trigger

---

## Live Evidence (when deployed)

- **Portfolio / trade history:** https://app.zerion.io/0xc1923710468607b8b7db38a6afbb9b432744390c/history?chain=base&type=trade
- **Telegram group (Fabiettus Frens):** https://t.me/+W6uDp6YN7nAxYzA0 — when the agent is running, the Watcher bot answers questions here.
- **ERC-8004 Agent Identity (Base):** https://basescan.org/token/0x8004a169fb4a3325136eb29fa0ceb6d2e539a432?a=0xc1923710468607b8b7db38a6afbb9b432744390c — registered AgentIdentity (AGENT) NFT holder.

---

## Tracks Applied To

### 🎯 Autonomous Trading Agent Track ($5,000)

| Requirement | How we meet it |
|-------------|----------------|
| Proven profitability metrics | Zerion portfolio live — https://app.zerion.io/0xc1923710468607b8b7db38a6afbb9b432744390c/history?chain=base&type=trade |
| Novel trading strategy implementation | Multi-factorial deterministic benchmarked strategy + Quant LLM with Kelly and EV (`strategy_factor_analysis`, `spawn_subagents` role `quant`) |
| Risk management system | Reserve recovery (USDC on Base), position caps (15% bluechip / 5% speculative), deployable caps (40% USDC / 70% source asset), Kelly sizing |
| Performance analytics dashboard | Zerion portfolio live — same link above |

### 🎯 "Let the Agent Cook" Track ($2,000)

| Requirement | How we meet it |
|-------------|----------------|
| Fully autonomous decision loop | `opportunity/cron.go` triggers proactive scans; agent pays for inference via x402; executes swaps without human-in-loop (below spend limit) |
| Self-improving capabilities | Failure analyzer subagent watches logs and errors (`FAILURE_ANALYZER=1`, `docs/tool-failure-learning.md`) |
| Autonomous goal-setting | Personality defines mission (grow capital, sustain runway); strategy defines opportunity categories and entry criteria; agent decides when to act |

### 🎯 ERC-8004 Identity Track ($2,000)

| Requirement | How we meet it |
|-------------|----------------|
| Complete identity framework integration | ERC-8004 AgentIdentity (AGENT) registered on Base — https://basescan.org/token/0x8004a169fb4a3325136eb29fa0ceb6d2e539a432?a=0xc1923710468607b8b7db38a6afbb9b432744390c |

---

## Security

- Private keys and API keys are never logged or echoed. See `SECURITY_ASSESSMENT.md` for gateway and approval flow details.
