# Distributed Rate Limiter 
**Live Demo:** [distributed-rate-limiter-35lf.onrender.com](https://distributed-rate-limiter-35lf.onrender.com)

A distributed rate limiting service built in Go. Multiple stateless API replicas sit behind an nginx load balancer and share rate-limit state through Redis 7+ Lua functions (`FCALL`), so the limit is enforced globally, not per-process. Redis itself runs as a Sentinel-managed master/replica pair for automatic failover. Each replica wraps its Redis calls in an in-memory circuit breaker for fail-open resilience during datastore outages.

---

## System Architecture

```text
                                    +-------------------+
                                    |    HTTP Client     |
                                    +-------------------+
                                              |
                                              v
                                    +-------------------+
                                    |   nginx (LB, :8080) |
                                    +-------------------+
                                     /        |         \
                                    v         v          v
                          +--------+  +--------+  +--------+
                          | api-1  |  | api-2  |  | api-3  |
                          | :8080  |  | :8080  |  | :8080  |
                          +--------+  +--------+  +--------+
                                     \        |        /
                                      \       |       /
                                   Rate Limit Middleware
                                   1. Extract client ID (IP / API key)
                                   2. Per-instance Circuit Breaker check
                                   3. FCALL sliding-window / token-bucket
                                      Lua function (atomic in Redis)
                                              |
                                              v
                          +------------------------------------+
                          |  Redis Sentinel (mymaster, quorum 2) |
                          |  sentinel-1  sentinel-2  sentinel-3   |
                          +------------------------------------+
                                   |                    |
                                   v                    v
                          +----------------+   +-----------------+
                          |  redis-master   |-->|  redis-replica  |
                          +----------------+   +-----------------+
                          (all state lives here - this is what makes
                           api-1/2/3 share one global rate-limit view)
```

Each API replica is a separate OS process with its own in-memory circuit breaker - it does **not** know about the other replicas directly. The only shared state is in Redis. That's the whole trick: correctness under concurrency comes from Redis executing each `FCALL` atomically and serially, not from any coordination between application instances.

---

## Core Features

- **Multiple Limiting Strategies**
  - **Sliding Window Counter** — smooths traffic across fixed window boundaries using a weighted estimate of the previous and current window counts.
  - **Token Bucket** — allows controlled bursts up to a capacity while refilling at a fixed rate per second.
- **Atomic Redis 7+ Execution** — Redis Function Libraries (`FCALL`) written in Lua evaluate and update state atomically. Redis's single-threaded execution model means this holds even when many API replicas call it concurrently for the same client — see [Concurrency Correctness](#concurrency-correctness).
- **Horizontally Scaled API** — 3 stateless replicas behind nginx by default. Add more by copying an `api-N` block in `docker-compose.yml`.
- **Redis High Availability** — master/replica Redis behind 3 Sentinels. The API connects via Sentinel and automatically reconnects to whichever node is promoted master after a failover.
- **Per-Instance Circuit Breaker** — each replica has its own `CLOSED → OPEN → HALF-OPEN` breaker around Redis calls, failing open (allowing traffic) when Redis is unreachable. See [Known Trade-offs](#known-trade-offs) for why this is per-instance rather than shared.
- **Standard HTTP Headers** — `X-Ratelimit-Remaining`, `Retry-After`, and `X-Served-By` (which replica handled the request — useful for proving distribution, see the load test tool below).
- **Prometheus Instrumentation** — request counts, blocked/allowed status, and per-instance circuit breaker state, exported at `/metrics`. No bundled Prometheus/Grafana stack — see [Observability](#observability) for why.

---

## Rate Limiting Algorithms

### 1. Sliding Window Counter

Estimates current request volume as a weighted sum of the previous window's count and the current window's count, avoiding the abrupt reset traffic sees at fixed boundaries:

$$\text{Estimated Count} = \text{Count}_{\text{prev}} \times \left(1 - \frac{t_{\text{current}}}{\text{Window Size}}\right) + \text{Count}_{\text{curr}}$$

- **Use case:** fixed thresholds where boundary spikes must be smooth (e.g. auth routes).
- **Route:** `/api/v1/auth/login` — 5 requests / 60 seconds (configurable via `RATE_LIMIT_REQUESTS` / `RATE_LIMIT_WINDOW_SECONDS`).

### 2. Token Bucket

Tokens refill at a constant rate up to a capacity; each request consumes one. Empty bucket → `429`.

- **Use case:** bursty traffic that shouldn't overwhelm downstream infrastructure.
- **Route:** `/api/v1/resource` — capacity 15, refill 2 tokens/sec.

---

## Getting Started

### Prerequisites

- Go 1.22+
- Docker and Docker Compose

### Local Installation & Setup

```bash
git clone https://github.com/your-username/rate-limiter.git
cd rate-limiter
cp .env.example .env
```

> **Note:** this repo ships `go.mod` but not `go.sum` — run `go mod tidy` once after cloning to resolve and lock dependency checksums.

Bring up the full stack — 3 API replicas, nginx, and Redis Sentinel HA:

```bash
docker compose up --build
```

| Service | Address |
|---|---|
| API (via nginx load balancer) | http://localhost:8080 |
| Individual replicas (debugging only, not exposed by default) | `docker compose exec api-1 wget -qO- localhost:8080/health` |

All client traffic should go through nginx on `:8080`. Hitting an individual `api-N` container directly bypasses the load balancer, which defeats the point of the multi-instance test below.

---

## Concurrency Correctness

The core claim of this project is: **N replicas, one globally-consistent limit.** Two ways to verify that:

### 1. Automated concurrency tests (`go test`)

`internal/limiter/token_bucket_test.go` and `sliding_window_counter_test.go` fire 50–100 goroutines at the *same identifier* simultaneously and assert the allowed count is exact:

```bash
docker run -d --rm -p 6379:6379 redis:7-alpine
TEST_REDIS_ADDR=localhost:6379 go test ./internal/limiter/... -run Concurrent -v -race
```

Expected: exactly `capacity` (token bucket) or `limit` (sliding window) requests allowed, no matter how many goroutines raced for them — proving `FCALL` execution is atomic under concurrency, not just under sequential calls.

### 2. Load test against the live multi-instance stack

```bash
docker compose up --build -d
go run ./cmd/loadtest -url http://localhost:8080/api/v1/resource -concurrency 50 -key demo-client
```

Example output:

```text
Fired 50 concurrent requests at http://localhost:8080/api/v1/resource in 210ms

  allowed (200):        15
  blocked (429):        35
  failed (network/etc): 0

  requests served per backend instance:
    api-1      17
    api-2      16
    api-3      17

  Multiple instances served this run and the allowed count above still matches the
  configured limit - confirming the limit is enforced globally via Redis, not per-process.
```

Exactly 15 requests allowed (the token bucket's capacity) even though the requests were spread across all three API replicas by nginx — this is the distributed guarantee in action, not a claim on paper.

---

## Known Trade-offs

Being upfront about what this design does and doesn't solve:

- **Circuit breaker state is per-instance, in-memory.** Each replica trips and recovers independently based on its own observed Redis failures — there's no shared "Redis is down" signal across replicas. This is deliberate: coordinating breaker state across instances would mean depending on the same datastore the breaker exists to protect against, trading a small window of inconsistency for not introducing a new failure mode. In a real outage, all replicas see failures at roughly the same time and converge to `OPEN` within a request cycle or two of each other. The `instance` label on `rate_limiter_circuit_breaker_state` makes this observable — you can watch `api-1` and `api-2` trip at slightly different moments in `/metrics`.
- **Fail-open under a Redis/Sentinel-quorum outage means rate limiting is temporarily disabled**, not that requests fail. This is a conscious availability-over-strictness choice, appropriate for this kind of traffic-shaping middleware but worth calling out explicitly — it would be the wrong choice for, say, a payment authorization check.
- **Sentinel failover has a detection window** (`down-after-milliseconds`, 5s here) — during that window the breaker on each replica will see failures and fail open, then recover once the new master is promoted and go-redis reconnects.

---

## Redis High Availability

Redis runs as one master, one replica, and 3 Sentinels (quorum 2) instead of a single instance. The API connects through Sentinel rather than a hardcoded address, so it always finds the current master.

**Test a failover:**

```bash
# Watch a sentinel promote the replica in real time
docker compose logs -f sentinel-1 &

# Kill the master
docker compose stop redis-master

# Within a few seconds, sentinel-1's logs show +sdown / +odown / +failover-triggered
# / +promoted-slave — redis-replica becomes the new master.

# Traffic continues to be rate-limited correctly throughout, aside from a brief
# fail-open window while Sentinel detects the failure:
go run ./cmd/loadtest -url http://localhost:8080/api/v1/resource -concurrency 20

# Bring the old master back - it rejoins as a replica of the new master
docker compose start redis-master
```

---

## Observability

`/metrics` exposes standard Prometheus metrics on every replica:

- `rate_limiter_requests_total{strategy, status, instance}` — request counts by strategy, `allowed`/`blocked`, and which replica handled it.
- `rate_limiter_circuit_breaker_state{name, instance}` — `0`=Closed, `1`=Half-Open, `2`=Open, per replica.
- `rate_limiter_redis_latency_seconds{strategy, instance}` — Redis operation latency histogram.

There's no bundled Prometheus server or Grafana dashboard in this repo. For a project this size, standing up a full monitoring stack that no one queries doesn't add much — the instrumentation itself is the useful, low-cost part. If you want to wire it up: point any Prometheus instance's scrape config at `api-1:8080/metrics`, `api-2:8080/metrics`, `api-3:8080/metrics` (or scrape through nginx, though that won't let you distinguish which instance is being polled).

---

## Testing & CI

```bash
go vet ./...
gofmt -l .                 # should print nothing

# Unit tests (no Redis required)
go test ./internal/middleware/... ./internal/limiter/... -run 'CircuitBreaker|ExtractIdentifier|RateLimit' -v

# Full suite including Redis-backed integration + concurrency tests
docker run -d --rm -p 6379:6379 redis:7-alpine
TEST_REDIS_ADDR=localhost:6379 go test ./... -v -race
```

`.github/workflows/ci.yml` runs this on every push/PR: `gofmt` check → `go vet` → build → full test suite (`-race`) against a `redis:7-alpine` service container. Test coverage:

| File | Covers |
|---|---|
| `circuit_breaker_test.go` | Fail-open behavior, breaker stops calling a dead datastore once tripped |
| `token_bucket_test.go` | Basic correctness + 100-way concurrent correctness on one identifier |
| `sliding_window_counter_test.go` | Basic correctness + 50-way concurrent correctness on one identifier |
| `ratelimit_test.go` | Middleware headers, 429 behavior, fail-open on limiter error, identifier extraction priority |

---

## Verification & Manual Testing

### Sliding window (single instance behavior)

```bash
for i in {1..8}; do curl -si http://localhost:8080/api/v1/auth/login | head -n1; done
```

Requests 1–5 return `200`, 6–8 return `429`.

### Token bucket burst handling

```bash
for i in {1..20}; do curl -si http://localhost:8080/api/v1/resource | head -n1; done
```

### Circuit breaker resilience

```bash
docker compose stop redis-master redis-replica sentinel-1 sentinel-2 sentinel-3
for i in {1..8}; do curl -si http://localhost:8080/api/v1/auth/login | head -n1; done
# All return 200 - each replica's breaker has tripped OPEN and is failing open.

docker compose start redis-master redis-replica sentinel-1 sentinel-2 sentinel-3
```

### Which instance served a request

```bash
curl -si http://localhost:8080/api/v1/resource | grep -i x-served-by
```

Run it a few times — you'll see `api-1`, `api-2`, `api-3` rotate as nginx round-robins.


---

## Live Deployment Verification

The service is also deployed on Render at `https://distributed-rate-limiter-35lf.onrender.com` as a single instance (Render's free tier runs one replica, not the 3-node setup used locally — this section verifies the live deployment works correctly, not the multi-instance distribution claim; see [Concurrency Correctness](#concurrency-correctness) for that).

### Health check

```bash
curl -i https://distributed-rate-limiter-35lf.onrender.com/health
```

```text
HTTP/2 200
{"status":"OK"}
```

### Sliding window (auth route)

```bash
for i in {1..8}; do
  curl -s -w "Request $i: HTTP %{http_code}\n" https://distributed-rate-limiter-35lf.onrender.com/api/v1/auth/login
done
```

Requests 1–5 return `200`, 6–8 return `429` — matches the 5 req/60s limit.

### Token bucket under concurrency

```bash
for i in {1..25}; do
  curl -s -w "%{http_code}\n" https://distributed-rate-limiter-35lf.onrender.com/api/v1/resource &
done; wait
```

25 requests fired concurrently against the live deployment; confirmed via `/metrics` immediately after:

```bash
curl -s https://distributed-rate-limiter-35lf.onrender.com/metrics | grep rate_limiter_requests_total
```

```text
rate_limiter_requests_total{instance="render-api-1",status="allowed",strategy="token_bucket_api"} 15
rate_limiter_requests_total{instance="render-api-1",status="blocked",strategy="token_bucket_api"} 10
```

Exactly 15 allowed (the token bucket's capacity), 10 blocked — correct under real concurrent load over the network, not just localhost.

### Load test tool against the live URL

`cmd/loadtest` works against any URL, not just the local stack:

```bash
go run ./cmd/loadtest -url https://distributed-rate-limiter-35lf.onrender.com/api/v1/resource -concurrency 30 -key live-test
```

```text
Fired 30 concurrent requests in 1.16s

  allowed (200):        17
  blocked (429):        13
  failed (network/etc): 0

  requests served per backend instance:
    render-api-1      17
```

17 rather than exactly 15 here is expected, not a bug: the token bucket refills on whole-second boundaries (`time.Now().Unix()`), so a burst spanning a one-second rollover picks up a couple of extra refilled tokens mid-run — the same effect covered in [Rate Limiting Algorithms](#rate-limiting-algorithms).


---

## Directory Structure

```text
.
├── cmd
│   ├── api
│   │   └── main.go                   # Entry point, router, Sentinel-aware Redis client
│   └── loadtest
│       └── main.go                   # Concurrent load tool proving cross-instance correctness
├── internal
│   ├── config
│   │   └── config.go                 # Env parsing (instance ID, standalone vs sentinel mode)
│   ├── limiter
│   │   ├── circuit_breaker.go        # Per-instance circuit breaker wrapper
│   │   ├── circuit_breaker_test.go
│   │   ├── limiter.go                # Strategy interface
│   │   ├── sliding_window_counter.go
│   │   ├── sliding_window_counter_test.go
│   │   ├── token_bucket.go
│   │   ├── token_bucket_test.go
│   │   └── testutil_test.go          # Shared test helpers (Redis connection, unique keys)
│   ├── metrics
│   │   └── metrics.go                # Prometheus collectors, instance-labeled
│   └── middleware
│       ├── ratelimit.go              # HTTP middleware
│       └── ratelimit_test.go
├── nginx
│   └── nginx.conf                    # Load balancer config, 3-way upstream
├── sentinel
│   └── sentinel.conf                 # Shared Sentinel config (quorum 2)
├── .github/workflows/ci.yml          # gofmt, vet, build, test (with Redis service container)
├── .env.example
├── docker-compose.yml                # 3 API replicas + nginx + Redis Sentinel HA
├── Dockerfile
├── go.mod
└── README.md
```
