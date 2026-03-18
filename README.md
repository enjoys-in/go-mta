# go-mta

High-performance, pluggable Go email delivery engine with asynq-backed persistent queue, per-domain throttling, MX caching, IP rotation, bounce detection, and event system.

## Prerequisites

- **Go 1.21+**
- **Dragonfly / Redis** — used for job queue (asynq), MX cache, rate limiting, IP/domain pool caching

```bash
# Start Dragonfly (or Redis) locally
docker run -d --name dragonfly -p 6379:6379 docker.dragonflydb.io/dragonflydb/dragonfly
```

---

## Project Structure

```
cmd/gomta/              CLI entry point (server + HTTP API)
internal/
  mta/                  Core MTA delivery engine (pure delivery pipe)
    config/             MTA config struct
    delivery/           Adapter interface + factory
      relay/            emersion/go-smtp relay (upstream SMTP)
      direct/           net/smtp direct MX delivery
      http/             HTTP webhook injection
  api/                  HTTP API (POST /api/v1/send, GET /api/v1/health)
pkg/
  server/               Orchestrator: pipeline, IP selection, domain routing
  queue/                Asynq-backed persistent job queue
  configloader/         TOML + env config loader
  resolver/             DNS resolver with MX caching (Dragonfly HSET, 12h cron)
  cache/                Dragonfly/Redis client
  rotation/             Round-robin IP rotator (IPv4/IPv6 family-aware)
  ippool/               IP source pool (from ips.toml)
  domainpool/           Per-domain delivery rules (from domains.toml)
  ratelimit/            Per-domain rate limiter (cache-backed)
  retry/                Exponential backoff retrier
  bounce/               SMTP bounce classifier (hard/soft)
  dsn/                  RFC 3464 Delivery Status Notification builder
  events/               Channel-based event dispatcher
  preflight/            Message validation (size, recipients)
  logger/               slog structured logger
  types/                Shared types, constants, sync.Pool
configs/
  config.example.toml   Main config template
  ips.toml              IP source pool definition
  domains.toml          Per-provider domain rules (Gmail, Outlook, Yahoo, etc.)
```

---

## Step 1: Configuration

### Option A: TOML Config Directory (recommended)

Create a config directory with three files:

**configs/config.toml**
```toml
method = "direct"                   # "relay", "direct", or "http"
local_ips = ["198.51.100.1"]

ip_lookup_strategy = "Ipv4AndIpv6"  # Ipv4Only, Ipv6Only, Ipv4AndIpv6, Ipv4ThenIpv6, Ipv6ThenIpv4

# Relay (only if method = "relay")
relay_host = "smtp.provider.com"
relay_port = 587
relay_user = "user@provider.com"
relay_pass = "secret"
relay_tls = false
relay_auth = true
relay_timeout = 30

# Direct delivery (only if method = "direct")
direct_port = 25
direct_helo = "mail.yourdomain.com"
direct_tls = true
direct_timeout = 30

# HTTP webhook (only if method = "http")
http_url = "https://hooks.example.com/ingest"
http_method = "POST"
http_timeout = 30
http_auth_type = "bearer"           # "bearer", "hmac-sha256", or ""
http_auth_secret = "your-token"

# TLS
tls_insecure_skip_verify = false
tls_ca_cert_file = ""

# Dragonfly / Redis
cache_addr = "127.0.0.1:6379"
cache_user = ""
cache_pass = ""
cache_db = 0

# Queue (asynq workers)
queue_workers = 4                   # 0 = runtime.NumCPU()

# Rate limiting
rate_limit_per_sec = 10             # per domain

# Retry
max_retries = 5

# Preflight
max_message_size = 26214400         # 25 MB
max_recipients = 100
```

**configs/ips.toml**
```toml
[[sources]]
name = "primary"
ip = "198.51.100.1"
weight = 2
ehlo_domain = "mail1.yourdomain.com"

[[sources]]
name = "secondary"
ip = "198.51.100.2"
weight = 1
ehlo_domain = "mail2.yourdomain.com"
```

**configs/domains.toml** — per-provider delivery rules:
```toml
[domains.default]
domain = "*"
max_deliveries_per_connection = 5
delivery_method = "direct"

[domains.gmail]
domain = ".gmail.com"
max_deliveries_per_connection = 10   # batch 10 rcpts per TCP connection

[domains.outlook]
domain = ".outlook.com"
max_deliveries_per_connection = 40

[domains.yahoo]
domain = ".yahoo.com"
max_deliveries_per_connection = 20
```

### Option B: Environment Variables

```bash
export GOMTA_METHOD=direct
export GOMTA_LOCAL_IPS=198.51.100.1,198.51.100.2
export GOMTA_DIRECT_HELO=mail.yourdomain.com
export GOMTA_DIRECT_TLS=true
export GOMTA_CACHE_ADDR=127.0.0.1:6379
export GOMTA_QUEUE_WORKERS=8
export GOMTA_RATE_LIMIT=10
export GOMTA_MAX_RETRIES=5
```

Full env var list: `GOMTA_METHOD`, `GOMTA_LOCAL_IPS`, `GOMTA_RELAY_HOST`, `GOMTA_RELAY_PORT`, `GOMTA_RELAY_USER`, `GOMTA_RELAY_PASS`, `GOMTA_RELAY_TLS`, `GOMTA_RELAY_AUTH`, `GOMTA_RELAY_TIMEOUT`, `GOMTA_DIRECT_PORT`, `GOMTA_DIRECT_HELO`, `GOMTA_DIRECT_TLS`, `GOMTA_DIRECT_TIMEOUT`, `GOMTA_HTTP_URL`, `GOMTA_HTTP_METHOD`, `GOMTA_HTTP_TIMEOUT`, `GOMTA_HTTP_AUTH_TYPE`, `GOMTA_HTTP_AUTH_SECRET`, `GOMTA_TLS_INSECURE_SKIP_VERIFY`, `GOMTA_TLS_CA_CERT_FILE`, `GOMTA_CACHE_ADDR`, `GOMTA_CACHE_USER`, `GOMTA_CACHE_PASS`, `GOMTA_CACHE_DB`, `GOMTA_QUEUE_WORKERS`, `GOMTA_RATE_LIMIT`, `GOMTA_MAX_RETRIES`, `GOMTA_MAX_MESSAGE_SIZE`, `GOMTA_MAX_RECIPIENTS`, `GOMTA_IP_LOOKUP_STRATEGY`, `GOMTA_IPS_FILE`, `GOMTA_DOMAINS_FILE`.

---

## Step 2: Build

```bash
go build -o gomta ./cmd/gomta
```

---

## Step 3: Run the Server

```bash
# From config directory (recommended — auto-discovers ips.toml, domains.toml)
./gomta --config-dir ./configs --listen :8080

# From single TOML file
./gomta --config ./configs/config.toml --listen :8080

# From environment only
./gomta --listen :8080
```

The server starts:
1. Connects to Dragonfly/Redis
2. Loads IP pool from `ips.toml` and warms cache
3. Loads domain rules from `domains.toml` and warms cache
4. Starts the asynq worker pool (persistent job queue)
5. Starts MX cache refresh cron (every 12 hours)
6. Starts the HTTP API on `--listen` address
7. Waits for SIGINT/SIGTERM for graceful shutdown

---

## Step 4: Send Emails via HTTP API

### POST /api/v1/send

Submit a raw RFC 5322 email for delivery:

```bash
curl -X POST http://localhost:8080/api/v1/send \
  -H "X-Mail-From: sender@yourdomain.com" \
  -H "X-Recipients: user1@gmail.com, user2@outlook.com, user3@yahoo.com" \
  -H "Content-Type: message/rfc822" \
  --data-binary @message.eml
```

**Response** (HTTP 202 Accepted):
```json
{
  "recipients": [
    {"id": "a1b2c3d4e5f6a1b2c3d4e5f6", "to": "user1@gmail.com", "status": "queued"},
    {"id": "b2c3d4e5f6a1b2c3d4e5f6a1", "to": "user2@outlook.com", "status": "queued"},
    {"id": "c3d4e5f6a1b2c3d4e5f6a1b2", "to": "user3@yahoo.com", "status": "queued"}
  ]
}
```

Each recipient gets its own asynq job with a unique tracking ID. Jobs are persisted in Redis — they survive server restarts.

### GET /api/v1/health

```bash
curl http://localhost:8080/api/v1/health
```

---

## Step 5: Use as a Go Library

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "os/signal"

    "github.com/enjoys-in/go-mta/pkg/configloader"
    "github.com/enjoys-in/go-mta/pkg/events"
    "github.com/enjoys-in/go-mta/pkg/server"
    "github.com/enjoys-in/go-mta/pkg/types"
)

func main() {
    // 1. Load config
    cfg, err := configloader.LoadDir("./configs")
    if err != nil {
        log.Fatal(err)
    }

    // 2. Create server
    srv, err := server.New(cfg)
    if err != nil {
        log.Fatal(err)
    }

    // 3. Register event listeners
    srv.Events().On(types.EventDeliverySuccess, func(evt events.Event) {
        fmt.Printf("[OK] job=%s to=%s attempts=%d\n", evt.JobID, evt.From, evt.Attempt)
    })
    srv.Events().On(types.EventDeliveryFailed, func(evt events.Event) {
        fmt.Printf("[FAIL] job=%s err=%v\n", evt.JobID, evt.Err)
    })
    srv.Events().On(types.EventBounceDetected, func(evt events.Event) {
        fmt.Printf("[BOUNCE] job=%s category=%s code=%s\n",
            evt.JobID, evt.Meta["bounce_category"], evt.Meta["smtp_code"])
    })

    // 4. Start
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
    defer stop()
    srv.Start(ctx)

    // 5. Submit emails
    msg := []byte("From: sender@yourdomain.com\r\nTo: user@gmail.com\r\nSubject: Hello\r\n\r\nBody here")
    jobID, err := srv.Submit("sender@yourdomain.com", []string{"user@gmail.com"}, msg)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Queued:", jobID)

    // 6. Wait for shutdown signal
    <-ctx.Done()
    srv.Shutdown(context.Background())
}
```

---

## Delivery Pipeline

When a job is processed, the pipeline does the following **per domain**:

```
Submit() → asynq queue (Redis) → worker picks up job
  │
  ├─ Group recipients by domain (gmail.com, outlook.com, ...)
  ├─ Per domain (concurrently):
  │   ├─ Rate limit check (cache-backed, per-domain)
  │   ├─ MX lookup (cached in Dragonfly HSET, 12h refresh)
  │   ├─ Pick source IP (family-aware: IPv4↔IPv4, IPv6↔IPv6, weighted 2:1 IPv6)
  │   ├─ Load domain rules (max_deliveries_per_connection from domains.toml)
  │   ├─ Chunk recipients (e.g. 10 per connection for Gmail)
  │   ├─ For each chunk (with 1s throttle between rounds):
  │   │   ├─ Build config with pre-resolved MX hosts
  │   │   ├─ Retry with exponential backoff
  │   │   └─ MTA adapter delivers (pure SMTP pipe):
  │   │       ├─ Dial → HELO → STARTTLS → MAIL FROM → RCPT TO → DATA → QUIT
  │   │       └─ Track per-recipient success/failure
  │   └─ Emit events (success, failure, bounce)
  └─ Return results
```

The MTA adapters are **pure delivery pipes** — they receive pre-resolved MX hosts, pre-selected source IPs, and pre-chunked recipient lists. All orchestration (caching, throttling, rate limiting, IP selection) lives in the pipeline.

---

## Delivery Methods

| Method | Adapter | Use Case |
|---|---|---|
| `direct` | `net/smtp` | Send directly to recipient MX servers |
| `relay` | `emersion/go-smtp` | Route through upstream SMTP relay (e.g. SES, SendGrid) |
| `http` | `net/http` | Push to HTTP webhook / injection API |

Per-domain override in `domains.toml`:
```toml
[domains.internal]
domain = ".mycompany.com"
delivery_method = "relay"          # internal mail goes through relay
```

---

## Events

| Event | Fired When |
|---|---|
| `mail.from` | Envelope sender set |
| `rcpt.to` | Each recipient added |
| `data.start` | Before DATA transmission |
| `data.end` | After DATA transmission |
| `delivery.attempt` | Before calling adapter |
| `delivery.success` | Delivery succeeded |
| `delivery.failed` | Delivery failed (after retries) |
| `retry.exhausted` | Max retries reached |
| `bounce.detected` | Hard/soft bounce classified |
| `rate.limited` | Domain rate limit hit |
| `queue.enqueued` | Job added to queue |
| `preflight.fail` | Message failed validation |
| `shutdown.start` | Graceful shutdown initiated |

---

## Key Features

- **Persistent queue** — asynq + Redis/Dragonfly. Jobs survive restarts. Built-in retry with exponential backoff and dead letter queue.
- **Per-domain rules** — `domains.toml` controls `max_deliveries_per_connection`, `delivery_method`, connection limits, warmup schedules per provider (Gmail, Outlook, Yahoo, etc.)
- **MX caching** — DNS MX lookups cached in Dragonfly HSET with 12-hour automatic refresh cron.
- **IP rotation** — Family-aware (IPv4→IPv4, IPv6→IPv6) weighted round-robin from `ips.toml`.
- **Rate limiting** — Per-domain, cache-backed sliding window.
- **Bounce detection** — Classifies SMTP 4xx/5xx responses as hard or soft bounces.
- **DSN generation** — RFC 3464 Delivery Status Notification builder (`pkg/dsn`).
- **Per-recipient tracking** — RCPT TO failures tracked individually; partial delivery reported via `MultiRcptError`.
- **TLS** — STARTTLS with optional `InsecureSkipVerify` and custom CA certificate.
- **Context support** — All adapters accept `context.Context` for cancellation/deadlines.
- **HTTP webhook auth** — Bearer token or HMAC-SHA256 signature for HTTP adapter.
