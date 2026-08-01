# Distributed Rate Limiter

A high-performance, distributed rate limiting service built in Go. This system implements multiple rate-limiting algorithms backed by Redis 7+ Lua functions, featuring an in-memory Circuit Breaker to ensure high availability and fail-open handling during datastore outages. Real-time system metrics are exported directly to Prometheus and visualised through Grafana dashboards.

---

## System Architecture

```text
                               +---------------------------------------+
                               |              HTTP Client               |
                               +---------------------------------------+
                                                   |
                                                   v
+---------------------------------------------------------------------------------------------------+
| Go API Service (Port 8080)                                                                         |
|                                                                                                     |
|   +-------------------------------------------------------------------------------------------+   |
|   | Rate Limit Middleware                                                                      |   |
|   |                                                                                             |   |
|   |   1. Extracts Client Identifier (IP / API Key)                                              |   |
|   |   2. Invokes Circuit Breaker Wrapper                                                        |   |
|   |      +---------------------------------------------------------------------------------+   |   |
|   |      | Circuit Breaker State Check                                                      |   |   |
|   |      |                                                                                   |   |   |
|   |      |  [STATE: OPEN] ----(Bypass Datastore / Fail-Open)-------------> Allow Request      |   |   |
|   |      |                                                                                   |   |   |
|   |      |  [STATE: CLOSED / HALF-OPEN]                                                      |   |   |
|   |      |        |                                                                          |   |   |
|   |      |        v                                                                          |   |   |
|   |      |  Execute Strategy FCALL (Sliding Window / Token Bucket)                            |   |   |
|   |      +---------------------------------------------------------------------------------+   |   |
|   +-------------------------------------------------------------------------------------------+   |
|                               |                                      |                             |
|                               v                                      v                             |
|             +----------------------------------+   +----------------------------------+            |
|             | Sliding Window Counter Strategy   |   |      Token Bucket Strategy       |            |
|             +----------------------------------+   +----------------------------------+            |
|                               |                                      |                             |
+-------------------------------|--------------------------------------|-----------------------------+
                                |                                      |
                         (Redis FCALL)                          (Redis FCALL)
                                |                                      |
                                v                                      v
                               +----------------------------------------+
                               |          Redis 7+ Container            |
                               |  (Executes registered Lua functions)   |
                               +----------------------------------------+

                                                ===

+---------------------------------------------------------------------------------------------------+
| Observability Stack                                                                                |
|                                                                                                     |
|   +-------------------+        Scrapes /metrics        +-------------------+                       |
|   |   Go Application  | <----------------------------- | Prometheus Server |                       |
|   |  Prometheus SDK   |                                |    (Port 9090)    |                       |
|   +-------------------+                                +-------------------+                       |
|                                                                  |                                  |
|                                                                  | PromQL Queries                   |
|                                                                  v                                  |
|                                                        +-------------------+                        |
|                                                        | Grafana Dashboard |                        |
|                                                        |    (Port 3000)    |                        |
|                                                        +-------------------+                        |
+---------------------------------------------------------------------------------------------------+
```

---

## Core Features

- **Multiple Limiting Strategies:**
  - **Sliding Window Counter:** Provides smooth rate limiting using a weighted execution algorithm over current and previous window buckets.
  - **Token Bucket:** Allows controlled bursts of traffic up to a maximum bucket capacity while continuously replenishing tokens at a fixed rate per second.
- **Atomic Redis 7+ Execution:** Uses Redis Function Libraries (`FCALL`) written in Lua to handle limit evaluation and bucket updates atomically, eliminating race conditions across multiple application instances.
- **Circuit Breaker Integration:** Wraps datastore calls in a state machine (`CLOSED`, `OPEN`, `HALF-OPEN`). If Redis fails or times out consistently, the breaker trips to `OPEN` and degrades gracefully by failing open, preventing datastore outages from causing application downtime.
- **Standard HTTP Headers:** Returns full rate limiting visibility to clients using standard HTTP headers (`X-Ratelimit-Remaining`, `Retry-After`).
- **Prometheus Observability:** Tracks allowed requests, blocked requests (HTTP 429), strategy execution metrics, and circuit breaker state transitions.

---

## Rate Limiting Algorithms

### 1. Sliding Window Counter

Rather than dropping traffic abruptly at fixed time boundaries, the sliding window algorithm estimates current request volume using a weighted sum of the previous window's count and the current window's count:

$$\text{Estimated Count} = \text{Count}_{\text{prev}} \times \left(1 - \frac{t_{\text{current}}}{\text{Window Size}}\right) + \text{Count}_{\text{curr}}$$

- **Use Case:** Fixed rate thresholds where boundary spikes must be smooth (e.g., authentication routes).
- **Endpoint Route:** `/api/v1/auth/login` (5 requests / 60 seconds).

### 2. Token Bucket

Tokens are added to a bucket at a constant refill rate per second up to a defined maximum capacity. Each incoming request consumes one token. If no tokens remain, the request is rejected with an HTTP 429 status code.

- **Use Case:** API routes that need to handle bursty traffic patterns without overwhelming downstream infrastructure.
- **Endpoint Route:** `/api/v1/resource` (Capacity: 15 tokens, Refill: 2 tokens/sec).

---

## Getting Started

### Prerequisites

- Go 1.22 or higher
- Docker and Docker Compose

### Local Installation & Setup

1. Clone the repository:

   ```bash
   git clone https://github.com/tanmay-exists/distributed-rate-limiter.git
   cd distributed-rate-limiter
   ```

2. Copy the environment template:

   ```bash
   cp .env.example .env
   ```

3. Build and launch the entire infrastructure stack (API, Redis, Prometheus, Grafana):

   ```bash
   docker compose up --build
   ```

4. The services will start on the following ports:

   | Service | URL |
   |---|---|
   | Go API Service | http://localhost:8080 |
   | Prometheus UI | http://localhost:9090 |
   | Grafana Dashboard | http://localhost:3000 |

---

## Observability & Monitoring

### Prometheus Metrics

The service exports standard Prometheus metrics at `http://localhost:8080/metrics`:

- `rate_limiter_requests_total`: Counter tracking total requests tagged by status (`allowed` or `blocked`) and strategy.
- `rate_limiter_circuit_breaker_state`: Gauge tracking current state (`0` = CLOSED, `1` = HALF-OPEN, `2` = OPEN).

### Grafana Dashboard Setup

1. Access Grafana at `http://localhost:3000` (Default credentials: `admin` / `admin`).
2. Add a new Prometheus Data Source with the URL:

   ```text
   http://prometheus:9090
   ```

3. Create a new dashboard and add a Time Series panel with the following PromQL query to visualize request throughput by status:

   ```promql
   sum(rate(rate_limiter_requests_total[1m])) by (status, strategy)
   ```

---

## Verification & Testing

### 1. Test Sliding Window Rate Limit

Run a loop to send 8 requests to the auth route (limit is 5):

```bash
for i in {1..8}; do curl -i http://localhost:8080/api/v1/auth/login; echo ""; done
```

Requests 1 through 5 will return `HTTP 200 OK`, while requests 6 through 8 will return `HTTP 429 Too Many Requests`.

### 2. Test Token Bucket Burst Handling

Run 20 rapid requests against the resource route (capacity is 15):

```bash
for i in {1..20}; do curl -i http://localhost:8080/api/v1/resource; echo ""; done
```

### 3. Test Circuit Breaker Resilience

Simulate a Redis failure to observe the circuit breaker fail-open behavior:

1. Stop the Redis container:

   ```bash
   docker compose stop redis
   ```

2. Issue multiple requests to trigger circuit failures:

   ```bash
   for i in {1..5}; do curl -i http://localhost:8080/api/v1/auth/login; echo ""; done
   ```

   Notice that after reaching the failure threshold, the circuit breaker transitions to `OPEN`. Subsequent requests bypass Redis, return `HTTP 200 OK`, and allow traffic to flow without blocking the application.

3. Restart Redis:

   ```bash
   docker compose start redis
   ```

---

## Directory Structure

```text
.
├── cmd
│   └── api
│       └── main.go                   # Application entry point & router setup
├── Dockerfile                        # Go application build file
├── docker-compose.yml                # Multi-container service orchestration
├── go.mod                            # Go module manifest
├── go.sum                            # Go module checksum lockfile
├── internal
│   ├── config
│   │   └── config.go                 # Environment & configuration parsing
│   ├── limiter
│   │   ├── circuit_breaker.go        # In-memory circuit breaker state machine
│   │   ├── limiter.go                # Strategy interface definition
│   │   ├── sliding_window_counter.go # Sliding window implementation with Redis Lua
│   │   └── token_bucket.go           # Token bucket implementation with Redis Lua
│   ├── metrics
│   │   └── metrics.go                # Prometheus custom collector definitions
│   └── middleware
│       └── ratelimit.go              # HTTP middleware for request interception
└── prometheus.yml                    # Prometheus scrape target configuration
```
