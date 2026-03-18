# go-mta

Pluggable Go email delivery library with queue, retry, rate limiting, bounce detection, and event system.

## Architecture

```
cmd/gomta/           CLI entry point
internal/mta/        Core MTA delivery engine (untouched plugin)
  config/            MTA config struct
  delivery/          Adapter interface + factory
    relay/           emersion/go-smtp relay
    direct/          net/smtp direct MX delivery
    http/            HTTP webhook injection
pkg/
  types/             Shared types, constants, sync.Pool
  events/            Channel-based event dispatcher
  logger/            slog structured logger
  cache/             Dragonfly/Redis cache client
  retry/             Exponential backoff retrier
  ratelimit/         Per-domain rate limiter (cache-backed)
  rotation/          Round-robin IP rotator
  queue/             Async delivery queue with worker pool
  bounce/            SMTP bounce classifier
  configloader/      TOML + env config loader
  preflight/         Message validation (size, recipients)
  server/            Orchestrator (ties everything together)
```

## Quick Start

```bash
# Config from TOML
./gomta --config config.toml

# Config from environment
GOMTA_METHOD=relay GOMTA_RELAY_HOST=smtp.example.com GOMTA_LOCAL_IPS=1.2.3.4 ./gomta
```

## Usage as Library

```go
import (
    "github.com/enjoys-in/go-mta/pkg/configloader"
    "github.com/enjoys-in/go-mta/pkg/server"
    "github.com/enjoys-in/go-mta/pkg/events"
    "github.com/enjoys-in/go-mta/pkg/types"
)

cfg := configloader.LoadEnv()
srv, _ := server.New(cfg)

srv.Events().On(types.EventDeliverySuccess, func(evt events.Event) {
    log.Println("delivered", evt.JobID)
})
srv.Events().On(types.EventRcptTo, func(evt events.Event) {
    log.Println("rcpt", evt.To)
})

srv.Start(ctx)
jobID, err := srv.Submit("from@example.com", []string{"to@example.com"}, rawMessage)
srv.Shutdown(ctx)
```

## Events

| Event | Fired When |
|---|---|
| `mail.from` | Envelope sender set |
| `rcpt.to` | Each recipient added |
| `data.start` | Before DATA transmission |
| `data.end` | After DATA transmission |
| `delivery.attempt` | Before calling mta.Send |
| `delivery.success` | Delivery succeeded |
| `delivery.failed` | Delivery failed (after retries) |
| `retry.scheduled` | Retry attempt scheduled |
| `retry.exhausted` | Max retries reached |
| `bounce.detected` | Hard/soft bounce classified |
| `rate.limited` | Domain rate limit hit |
| `queue.enqueued` | Job added to queue |
| `queue.dequeued` | Job picked up by worker |
| `preflight.fail` | Message failed validation |
| `shutdown.start` | Graceful shutdown initiated |
| `shutdown.done` | Shutdown complete |

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `GOMTA_METHOD` | `direct` | relay / direct / http |
| `GOMTA_LOCAL_IPS` | `0.0.0.0` | Comma-separated local IPs |
| `GOMTA_RELAY_HOST` | | SMTP relay hostname |
| `GOMTA_RELAY_PORT` | `587` | SMTP relay port |
| `GOMTA_CACHE_ADDR` | `127.0.0.1:6379` | Dragonfly/Redis address |
| `GOMTA_QUEUE_WORKERS` | `4` | Concurrent delivery workers |
| `GOMTA_RATE_LIMIT` | `10` | Max sends per second per domain |
| `GOMTA_MAX_RETRIES` | `5` | Max retry attempts |
