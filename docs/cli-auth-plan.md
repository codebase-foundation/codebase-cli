# CLI Authentication — Future Plan

## Goal
Let CLI users authenticate with the Codebase web app so usage is tied to their account and credits.

## Current State
- Appwrite handles web auth (email/password, Google OAuth, GitHub OAuth, Web3 wallet)
- PostgreSQL credit system tracks usage per user_id with plans (free → pro)
- Agent API at `/api/agent/build` has bearer token auth via `AGENT_API_KEYS`
- CLI is standalone — no user identity, no credit tracking

## Approach: API Key + Browser OAuth

### API Key from Dashboard (fallback for headless/SSH)
- New DB table: `cli_api_keys` (key_hash, user_id, name, created_at, last_used)
- New backend endpoints: `POST /cli/keys` (create), `DELETE /cli/keys/:id` (revoke), `GET /cli/keys` (list)
- New settings UI: "CLI API Keys" section with generate/revoke
- CLI: `codebase auth <key>` stores token in `~/.config/codebase/credentials.json`
- CLI sends `Authorization: Bearer <key>` → backend proxies to LLM + deducts credits

### Browser OAuth (primary flow)
- `codebase auth login` spins up temp HTTP server on `localhost:{random_port}`
- Opens `https://staging.polyvibe.io/cli/auth?redirect_uri=http://localhost:{port}/callback`
- Web app does Appwrite OAuth → mints CLI token → redirects to localhost
- CLI receives token, stores in `~/.config/codebase/credentials.json`
- Same backend proxy + credit deduction as API key path

### Backend Proxy (both flows need this)
```
CLI → POST /cli/chat/completions (with Bearer token)
    → Backend validates token, checks credits
    → Backend proxies to real LLM provider
    → Backend logs usage, deducts credits
    → Streams response back to CLI
```

## Estimate
~5-7 days for both auth flows + proxy + credit integration + settings UI
