package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/types"
)

const (
	// TypeDelivery is the asynq task type for email delivery jobs.
	TypeDelivery = "email:deliver"
)

// WorkerFunc processes a job. Return error to signal failure.
type WorkerFunc func(ctx context.Context, job *types.Job) error

// jobPayload is the JSON-serialised form stored in Redis.
type jobPayload struct {
	ID         string    `json:"id"`
	From       string    `json:"from"`
	To         []string  `json:"to"`
	LocalIP    string    `json:"local_ip"`
	Data       []byte    `json:"data"`
	Method     string    `json:"method"`
	MaxRetries int       `json:"max_retries"`
	CreatedAt  time.Time `json:"created_at"`
}

// Queue wraps asynq client (enqueue) and server (dequeue + process).
type Queue struct {
	client *asynq.Client
	server *asynq.Server
	mux    *asynq.ServeMux
	fn     WorkerFunc
	log    *logger.Logger
}

// RedisConfig holds the connection details for the asynq broker.
type RedisConfig struct {
	Addr     string
	Username string
	Password string
	DB       int
}

// New creates an asynq-backed queue.
// workers is the server concurrency; queueSize is used for strict queue capacity.
func New(redisCfg RedisConfig, workers int, fn WorkerFunc) *Queue {
	if workers <= 0 {
		workers = types.DefaultQueueWorkers
	}

	redisOpt := asynq.RedisClientOpt{
		Addr:     redisCfg.Addr,
		Username: redisCfg.Username,
		Password: redisCfg.Password,
		DB:       redisCfg.DB,
	}

	client := asynq.NewClient(redisOpt)

	srv := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: workers,
		Queues:      map[string]int{"default": 1},
		RetryDelayFunc: func(n int, _ error, _ *asynq.Task) time.Duration {
			// Exponential backoff: 5s, 10s, 20s, 40s, ...
			d := time.Duration(1<<uint(n)) * 5 * time.Second
			if d > 10*time.Minute {
				d = 10 * time.Minute
			}
			return d
		},
		Logger: newAsynqLogger(),
	})

	q := &Queue{
		client: client,
		server: srv,
		mux:    asynq.NewServeMux(),
		fn:     fn,
		log:    logger.New(types.ComponentQueue),
	}
	q.mux.HandleFunc(TypeDelivery, q.handleTask)
	return q
}

// Start launches the asynq worker server (non-blocking).
func (q *Queue) Start(_ context.Context) {
	go func() {
		if err := q.server.Run(q.mux); err != nil {
			q.log.Error("asynq server error", err)
		}
	}()
	q.log.Info("asynq queue started")
}

// Enqueue serialises a Job and pushes it into the asynq broker.
func (q *Queue) Enqueue(job *types.Job) bool {
	payload, err := json.Marshal(jobPayload{
		ID:         job.Message.ID,
		From:       job.Message.From,
		To:         job.Message.To,
		LocalIP:    job.Message.LocalIP,
		Data:       job.Message.Data,
		Method:     job.Method,
		MaxRetries: job.MaxRetries,
		CreatedAt:  job.CreatedAt,
	})
	if err != nil {
		q.log.Error("failed to marshal job", err, slog.String("job_id", job.Message.ID))
		return false
	}

	task := asynq.NewTask(TypeDelivery, payload,
		asynq.MaxRetry(job.MaxRetries),
		asynq.Queue("default"),
		asynq.TaskID(job.Message.ID),
		asynq.Retention(24*time.Hour),
	)

	if _, err := q.client.Enqueue(task); err != nil {
		q.log.Error("failed to enqueue", err, slog.String("job_id", job.Message.ID))
		return false
	}
	return true
}

// Stop shuts down both client and server.
func (q *Queue) Stop() {
	q.server.Shutdown()
	q.client.Close()
	q.log.Info("asynq queue stopped")
}

// handleTask is the asynq handler that deserialises the payload and calls the WorkerFunc.
func (q *Queue) handleTask(ctx context.Context, t *asynq.Task) error {
	var p jobPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("queue: unmarshal payload: %w", err)
	}

	msg := types.AcquireMessage()
	msg.ID = p.ID
	msg.From = p.From
	msg.To = append(msg.To, p.To...)
	msg.LocalIP = p.LocalIP
	msg.Data = append(msg.Data, p.Data...)
	msg.Size = len(p.Data)
	msg.CreatedAt = p.CreatedAt

	job := types.AcquireJob()
	job.Message = msg
	job.Method = p.Method
	job.MaxRetries = p.MaxRetries
	job.CreatedAt = p.CreatedAt

	return q.fn(ctx, job)
}

// asynqLogAdapter wraps slog for asynq's Logger interface.
type asynqLogAdapter struct {
	log *logger.Logger
}

func newAsynqLogger() *asynqLogAdapter {
	return &asynqLogAdapter{log: logger.New(types.ComponentQueue)}
}

func (a *asynqLogAdapter) Debug(args ...interface{}) {
	a.log.Debug(fmt.Sprint(args...))
}
func (a *asynqLogAdapter) Info(args ...interface{}) {
	a.log.Info(fmt.Sprint(args...))
}
func (a *asynqLogAdapter) Warn(args ...interface{}) {
	a.log.Warn(fmt.Sprint(args...))
}
func (a *asynqLogAdapter) Error(args ...interface{}) {
	a.log.Error(fmt.Sprint(args...), nil)
}
func (a *asynqLogAdapter) Fatal(args ...interface{}) {
	a.log.Error(fmt.Sprint(args...), nil)
}
