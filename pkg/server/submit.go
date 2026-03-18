package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/enjoys-in/go-mta/pkg/events"
	"github.com/enjoys-in/go-mta/pkg/types"
)

// Submit creates a delivery job and enqueues it.
// Returns the generated job ID or an error if preflight fails.
func (s *Server) Submit(from string, to []string, data []byte) (string, error) {
	msg := types.AcquireMessage()
	msg.ID = generateID()
	msg.From = from
	msg.To = append(msg.To, to...)
	msg.Data = append(msg.Data, data...)
	msg.Size = len(data)
	msg.LocalIP = s.rotator.Next()
	msg.CreatedAt = time.Now()

	// Preflight
	if err := s.preflight.Check(msg); err != nil {
		s.events.Emit(events.Event{
			Type:      types.EventPreflightFail,
			Timestamp: time.Now(),
			JobID:     msg.ID,
			From:      from,
			Method:    s.cfg.Method,
			Err:       err,
		})
		types.ReleaseMessage(msg)
		return "", err
	}

	job := types.AcquireJob()
	job.Message = msg
	job.Method = s.cfg.Method
	job.MaxRetries = s.cfg.MaxRetries
	job.CreatedAt = time.Now()

	s.events.Emit(events.Event{
		Type:      types.EventQueued,
		Timestamp: time.Now(),
		JobID:     msg.ID,
		From:      from,
		LocalIP:   msg.LocalIP,
		Method:    s.cfg.Method,
	})

	if !s.queue.Enqueue(job) {
		types.ReleaseJob(job)
		return "", fmt.Errorf("server: queue full")
	}
	return msg.ID, nil
}

func generateID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}
