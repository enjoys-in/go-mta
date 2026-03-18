package types

import (
	"sync"
	"time"
)

// Message represents an email message flowing through the system.
type Message struct {
	ID        string
	From      string
	To        []string
	LocalIP   string
	Data      []byte
	Size      int
	CreatedAt time.Time
}

// Reset clears a Message for reuse via sync.Pool.
func (m *Message) Reset() {
	m.ID = ""
	m.From = ""
	m.To = m.To[:0]
	m.LocalIP = ""
	m.Data = m.Data[:0]
	m.Size = 0
	m.CreatedAt = time.Time{}
}

// Job wraps a Message with delivery metadata.
type Job struct {
	Message    *Message
	Method     string
	Attempt    int
	MaxRetries int
	NextRetry  time.Time
	CreatedAt  time.Time
	LastErr    error
}

// Reset clears a Job for reuse via sync.Pool.
func (j *Job) Reset() {
	if j.Message != nil {
		j.Message.Reset()
	}
	j.Method = ""
	j.Attempt = 0
	j.MaxRetries = 0
	j.NextRetry = time.Time{}
	j.CreatedAt = time.Time{}
	j.LastErr = nil
}

// Result holds the outcome of a delivery attempt.
type Result struct {
	JobID     string
	Success   bool
	Err       error
	Attempt   int
	Duration  time.Duration
	Timestamp time.Time
}

// RcptResult holds per-recipient delivery status.
type RcptResult struct {
	Address string
	Code    int
	Msg     string
	Err     error
}

// --- sync.Pool for low allocation ---

var messagePool = sync.Pool{
	New: func() any {
		return &Message{
			To:   make([]string, 0, 8),
			Data: make([]byte, 0, 4096),
		}
	},
}

// AcquireMessage gets a Message from the pool.
func AcquireMessage() *Message {
	return messagePool.Get().(*Message)
}

// ReleaseMessage returns a Message to the pool after reset.
func ReleaseMessage(m *Message) {
	m.Reset()
	messagePool.Put(m)
}

var jobPool = sync.Pool{
	New: func() any {
		return &Job{}
	},
}

// AcquireJob gets a Job from the pool.
func AcquireJob() *Job {
	return jobPool.Get().(*Job)
}

// ReleaseJob returns a Job to the pool after reset.
func ReleaseJob(j *Job) {
	j.Reset()
	jobPool.Put(j)
}

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 4096)
		return &b
	},
}

// AcquireBuffer gets a []byte buffer from the pool.
func AcquireBuffer() *[]byte {
	return bufferPool.Get().(*[]byte)
}

// ReleaseBuffer returns a []byte buffer to the pool.
func ReleaseBuffer(b *[]byte) {
	*b = (*b)[:0]
	bufferPool.Put(b)
}
