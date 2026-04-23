
# Ethereum Wallet



## Introduction

This Go program generates Ethereum key pairs at maximum speed, checks their balances via a **local Ethereum full node**, and stores accounts with a non-zero balance in a MySQL database.

> **This tool requires a local Ethereum full node. Public RPC endpoints are not supported and would make the scan effectively useless due to rate limits.**

Balance queries are sent in batches via `eth_getBalance` JSON-RPC `BatchCallContext`, eliminating per-request HTTP overhead.


## Prerequisites

- Golang 1.22
```bash
$ snap install go --classic
```
- Docker & Docker Swarm https://docs.docker.com/engine/install
- A **local Ethereum full node** — [Erigon](https://github.com/erigontech/erigon) is recommended for superior random-state-access performance (MDBX flat storage). Geth also works.
  - Erigon quickstart: https://docs.erigon.tech/getting-started/install
  - Ensure the node is fully synced and either:
    - The IPC socket is accessible (e.g. `/data/erigon/ethereum/geth.ipc`) — **preferred, lowest latency**
    - Or the JSON-RPC HTTP/WebSocket endpoint is reachable (e.g. `http://127.0.0.1:8545`)


## Environment Variables

| Variable          | Required | Default            | Description                                                                 |
|-------------------|----------|--------------------|-----------------------------------------------------------------------------|
| `DB_USERNAME`     | yes      | —                  | MySQL username                                                              |
| `DB_PASSWORD`     | yes      | —                  | MySQL password                                                              |
| `DB_HOST`         | yes      | —                  | MySQL host                                                                  |
| `DB_PORT`         | yes      | —                  | MySQL port                                                                  |
| `DB_SCHEMA`       | yes      | —                  | MySQL database name                                                         |
| `SERVER_PORT`     | yes      | —                  | HTTP health-check port                                                      |
| `ETH_NODE_URL`    | yes      | —                  | Local Ethereum node: IPC path, `ws://`, `wss://`, or `http://` URL         |
| `BATCH_SIZE`      | no       | `500`              | Number of `eth_getBalance` calls per JSON-RPC batch request                |
| `RPC_CONCURRENCY` | no       | `32`               | Number of concurrent batch requests in flight                              |
| `KEYGEN_WORKERS`  | no       | `runtime.NumCPU()` | Number of goroutines generating key pairs                                  |


## Deploy Ethereum Wallet

#### Clone the repository into a local directory

```bash
$ git clone https://github.com/palmatovic/ethereum-wallet.git
```


#### Navigate to the project directory:

```bash
$ cd ethereum-wallet
```

#### Build image
```bash
$ docker build -t wallet:latest -f image/Dockerfile .
```

#### Review ethereum.yml, replace following values
``` bash
- <YOUR_LOCALNET_IP>
- <SERVER_PORT>
- <ETH_NODE_URL>   # e.g. /data/erigon/ethereum/geth.ipc  or  http://127.0.0.1:8545
```

#### Deploy
```bash
$ docker stack deploy -c docker/ethereum.yml ethereum
```


## Optional - Dashboard

#### Clone the repository into a local directory

```bash
$ cd Homepage
```

#### Review homepage.yml, replace following value
``` bash
- <UI_PORT>
```

#### Deploy
```bash
$ docker stack deploy -c homepage.yml homepage
```


## Disclaimer

The Ethereum private-key space is 2²⁵⁶ ≈ 10⁷⁷. Even scanning at 1 000 000 addresses/second against all ~3 × 10⁸ funded accounts on mainnet, the expected time to find a collision exceeds 10³⁰ years. **This tool is educational and experimental — it is not a realistic method for finding accounts with a balance.** Use it to understand Ethereum key generation, batch RPC, and Go concurrency patterns.


## Authors

- [@palmatovic](https://www.github.com/palmatovic)

