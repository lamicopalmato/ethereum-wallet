
# Ethereum Wallet



## Introduction

This Go program generates Ethereum key pairs at maximum speed, checks their balances via an Ethereum JSON-RPC node, and stores accounts with a non-zero balance in a MySQL database.

Balance queries are sent in batches via `eth_getBalance` JSON-RPC `BatchCallContext`, eliminating per-request HTTP overhead.
For best performance use a **local Ethereum full node** (Option A below).
For quick testing without disk requirements, a **public RPC endpoint** is also supported (Option B).


## Prerequisites

### Common requirements
- Docker & Docker Compose v2: https://docs.docker.com/engine/install

### Option A — Local geth node
- ~500 GB free NVMe SSD (required for Ethereum state)
- First sync takes 1–3 days; geth runs inside the compose stack

### Option B — Public RPC endpoint
- No SSD required
- An internet-accessible JSON-RPC endpoint (default: `https://ethereum-rpc.publicnode.com`, no API key)
- Optional: API key for Alchemy / Infura / QuickNode for higher throughput


## Environment Variables

| Variable           | Required | Default      | Description                                                                |
|--------------------|----------|--------------|----------------------------------------------------------------------------|
| `ETH_NODE_URL`     | yes      | `http://geth:8545` | Ethereum JSON-RPC endpoint (IPC path, `ws://`, `wss://`, or `http://` URL)|
| `BATCH_SIZE`       | no       | `500`        | Number of `eth_getBalance` calls per JSON-RPC batch request. **Set to `100` for public RPC mode.** |
| `RPC_CONCURRENCY`  | no       | `32`         | Number of concurrent batch requests in flight. **Set to `4` for public RPC mode.** |
| `KEYGEN_WORKERS`   | no       | `runtime.NumCPU()` | Number of goroutines generating key pairs                             |
| `DB_USERNAME`      | yes      | `${MYSQL_USER}`    | MySQL username                                                        |
| `DB_PASSWORD`      | yes      | `${MYSQL_PASSWORD}` | MySQL password                                                       |
| `DB_HOST`          | yes      | `database`   | MySQL host                                                                 |
| `DB_PORT`          | yes      | `3306`       | MySQL port                                                                 |
| `DB_SCHEMA`        | yes      | `${MYSQL_DATABASE}` | MySQL database name                                                   |
| `SERVER_PORT`      | yes      | `8080`       | HTTP health-check port                                                     |


## Deploy

Copy the environment template and edit as needed:

```bash
cp .env.example .env
# edit .env as needed
```

### Option A — Local geth node (recommended, requires ~500 GB NVMe SSD)

Runs a self-hosted geth inside the compose stack.
First sync takes 1–3 days.
Highest throughput (~500k–1M balance checks/s with batching).

```bash
docker compose --profile local up -d --build
```

> **Note**: Leave `ETH_NODE_URL=http://geth:8545` (the default) in `.env`.
> `BATCH_SIZE=500` and `RPC_CONCURRENCY=32` are the recommended defaults for a local node.

### Option B — Public RPC (demo / quick start, no SSD required)

Uses a remote JSON-RPC endpoint (default: `publicnode.com`, no API key).
Throughput is limited to ~5k–10k checks/s by the provider's rate limits.
For higher throughput, set `PUBLIC_ETH_NODE_URL` in `.env` to an Alchemy/Infura URL.

In `.env`, set:
```
ETH_NODE_URL=https://ethereum-rpc.publicnode.com
BATCH_SIZE=100
RPC_CONCURRENCY=4
```

Then run:
```bash
docker compose --profile public up -d --build
```

> **Note**: public RPC providers may rate-limit or ban automated scanning.
> This mode is intended for testing and demos, not for serious key-space exploration.
> The wallet client automatically retries on HTTP 429 / JSON-RPC -32005 with exponential backoff.


## Disclaimer

The Ethereum private-key space is 2²⁵⁶ ≈ 10⁷⁷. Even scanning at 1 000 000 addresses/second against all ~3 × 10⁸ funded accounts on mainnet, the expected time to find a collision exceeds 10³⁰ years. **This tool is educational and experimental — it is not a realistic method for finding accounts with a balance.** Use it to understand Ethereum key generation, batch RPC, and Go concurrency patterns.


## Authors

- [@palmatovic](https://www.github.com/palmatovic)

