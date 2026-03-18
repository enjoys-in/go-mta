package types

// Delivery methods.
const (
	MethodRelay  = "relay"
	MethodDirect = "direct"
	MethodHTTP   = "http"
)

// Event type constants.
const (
	EventMailFrom        = "mail.from"
	EventRcptTo          = "rcpt.to"
	EventDataStart       = "data.start"
	EventDataEnd         = "data.end"
	EventDeliveryAttempt = "delivery.attempt"
	EventDeliverySuccess = "delivery.success"
	EventDeliveryFailed  = "delivery.failed"
	EventRetryScheduled  = "retry.scheduled"
	EventRetryExhausted  = "retry.exhausted"
	EventBounceDetected  = "bounce.detected"
	EventRateLimited     = "rate.limited"
	EventQueued          = "queue.enqueued"
	EventDequeued        = "queue.dequeued"
	EventPreflightFail   = "preflight.fail"
	EventShutdownStart   = "shutdown.start"
	EventShutdownDone    = "shutdown.done"
)

// Delivery status codes.
const (
	StatusPending   = "pending"
	StatusQueued    = "queued"
	StatusSending   = "sending"
	StatusSent      = "sent"
	StatusDeferred  = "deferred"
	StatusBounced   = "bounced"
	StatusFailed    = "failed"
	StatusRateLimit = "rate_limited"
)

// SMTP reply code classes.
const (
	SMTPClassSuccess   = 2
	SMTPClassTransient = 4
	SMTPClassPermanent = 5
)

// Bounce classification.
const (
	BounceHard = "hard"
	BounceSoft = "soft"
)

// Default limits.
const (
	DefaultMaxMessageSize  = 25 * 1024 * 1024 // 25 MB
	DefaultMaxRetries      = 5
	DefaultMaxRecipients   = 100
	DefaultQueueWorkers    = 4
	DefaultQueueSize       = 10000
	DefaultRateLimitPerSec = 10
)

// Logger components.
const (
	ComponentServer     = "server"
	ComponentQueue      = "queue"
	ComponentRelay      = "relay"
	ComponentDirect     = "direct"
	ComponentHTTP       = "http"
	ComponentRetry      = "retry"
	ComponentRateLimit  = "ratelimit"
	ComponentCache      = "cache"
	ComponentPreflight  = "preflight"
	ComponentBounce     = "bounce"
	ComponentRotation   = "rotation"
	ComponentResolver   = "resolver"
	ComponentIPPool     = "ippool"
	ComponentDomainPool = "domainpool"
)
