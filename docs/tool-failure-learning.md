# Tool failure learning (validation)

Phased rollout: JSONL logs under `{DATA_ROOT}/tool-failures/failures.jsonl`, optional auto-retry for transient tool errors, optional `FAILURE_ANALYZER` subagent, and `COMMON_ISSUES.md` (DataRoot or repo root).

## Phase 1 — logging

- Trigger a bad `spawn_subagents` payload (invalid JSON) and confirm one `spawn_subagents_error` line with redacted message.
- From a subagent path, call a forbidden tool and confirm `subagent_denied`.
- Main agent: invalid tool JSON / unknown tool → `exec_error` with session fields when available.

## Phase 2 — deterministic retry

- `http_request` or `web_search` returning an `Error:` containing `timeout` or `429`: first attempt logged, second succeeds → a `recovery` entry with the same correlation id family (or preceding attempts visible in JSONL).
- Terminal cases (unknown tool, invalid JSON args, wallet not configured): no auto-retry; single failure line.

## Phase 3 — failure analyzer

- Set `FAILURE_ANALYZER=1` and optional `X402_MODEL_ANALYZER` (autonomous x402). After a **non-terminal** tool failure, at most **one** analyzer run per user message; a system hint may follow tool results in the same turn.
- High-confidence `append_common_issue` appends a redacted line to `{DATA_ROOT}/COMMON_ISSUES.md`.

## Evidence threshold (when to enable analyzer)

Enable `FAILURE_ANALYZER` when JSONL shows repeated **non-terminal** failures (same tool + similar message) that auto-retry does not fix—otherwise keep analyzer off to save cost.

## Success criteria

- Failures are logged once per attempt (redacted); retries stop at the configured cap; recoveries are visible in the log; `COMMON_ISSUES.md` stays short when loaded into the prompt.
