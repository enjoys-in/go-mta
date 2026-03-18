package queue

import (
	"context"
	"log/slog"
	"sync"

	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/types"
)

// WorkerFunc processes a job. Return error to signal failure.
type WorkerFunc func(ctx context.Context, job *types.Job) error

// Queue is a bounded, in-memory async delivery queue with a worker pool.
type Queue struct {
	jobs    chan *types.Job
	workers int
	fn      WorkerFunc
	log     *logger.Logger
	wg      sync.WaitGroup
	cancel  context.CancelFunc
}

// New creates a queue. bufSize is the channel capacity, workers is concurrency.
func New(bufSize int, workers int, fn WorkerFunc) *Queue {
	if bufSize <= 0 {
		bufSize = types.DefaultQueueSize
	}
	if workers <= 0 {
		workers = types.DefaultQueueWorkers
	}
	return &Queue{
		jobs:    make(chan *types.Job, bufSize),
		workers: workers,
		fn:      fn,
		log:     logger.New(types.ComponentQueue),
	}
}

// Start launches the worker pool. Call Stop() to shut down.
func (q *Queue) Start(parentCtx context.Context) {
	ctx, cancel := context.WithCancel(parentCtx)
	q.cancel = cancel

	for i := 0; i < q.workers; i++ {
		q.wg.Add(1)
		go q.worker(ctx, i)
	}
	q.log.Info("queue started",
		slog.Int("workers", q.workers),
		slog.Int("buffer", cap(q.jobs)),
	)
}

// Enqueue adds a job to the queue. Returns false if the queue is full.
func (q *Queue) Enqueue(job *types.Job) bool {
	select {
	case q.jobs <- job:
		return true
	default:
		q.log.Warn("queue full, dropping job",
			slog.String("job_id", job.Message.ID),
		)
		return false
	}
}

// Len returns the number of jobs currently buffered.
func (q *Queue) Len() int {
	return len(q.jobs)
}

// Stop signals all workers to finish and waits for drain.
func (q *Queue) Stop() {
	q.cancel()
	close(q.jobs)
	q.wg.Wait()
	q.log.Info("queue stopped")
}

func (q *Queue) worker(ctx context.Context, id int) {
	defer q.wg.Done()
	for {
		select {
		case job, ok := <-q.jobs:
			if !ok {
				return // channel closed
			}
			if err := q.fn(ctx, job); err != nil {
				q.log.Error("worker job failed", err,
					slog.Int("worker_id", id),
					slog.String("job_id", job.Message.ID),
				)
			}
		case <-ctx.Done():
			// Drain remaining jobs in channel before exit.
			for job := range q.jobs {
				if err := q.fn(context.Background(), job); err != nil {
					q.log.Error("drain job failed", err,
						slog.Int("worker_id", id),
						slog.String("job_id", job.Message.ID),
					)
				}
			}
			return
		}
	}
}
