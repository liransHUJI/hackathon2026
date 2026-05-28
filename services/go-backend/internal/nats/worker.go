package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/hnweb/provenance/internal/models"
)

type HandlerFunc func(context.Context, []byte) error

type Worker struct {
	client *Client
	store  interface {
		IsCancelled(context.Context, string) (bool, error)
	}
	logger  *slog.Logger
	subject string
	durable string
	handler HandlerFunc
}

func NewWorker(deps WorkerDependencies, subject, durable string, handler HandlerFunc) *Worker {
	return &Worker{
		client:  deps.NATS,
		store:   deps.Store,
		logger:  deps.Logger,
		subject: subject,
		durable: durable,
		handler: handler,
	}
}

func (w *Worker) Start(ctx context.Context) error {
	sub, err := w.client.js.PullSubscribe(
		w.subject,
		w.durable,
		natsgo.BindStream(StreamName),
		natsgo.ManualAck(),
		natsgo.AckWait(2*time.Minute),
		natsgo.MaxDeliver(5),
	)
	if err != nil {
		return err
	}

	go func() {
		for ctx.Err() == nil {
			msgs, err := sub.Fetch(8, natsgo.MaxWait(time.Second))
			if err != nil {
				if err != natsgo.ErrTimeout {
					w.logger.Warn("worker fetch failed", "subject", w.subject, "error", err)
				}
				continue
			}
			for _, msg := range msgs {
				w.handleMessage(ctx, msg)
			}
		}
	}()
	return nil
}

func (w *Worker) handleMessage(ctx context.Context, msg *natsgo.Msg) {
	jobID := envelopeJobID(msg.Data)
	if jobID != "" && w.store != nil {
		cancelled, err := w.store.IsCancelled(ctx, jobID)
		if err == nil && cancelled {
			_ = msg.Ack()
			return
		}
	}
	if err := w.handler(ctx, msg.Data); err != nil {
		w.logger.Error("worker handler failed", "subject", w.subject, "error", err)
		if metadata, metaErr := msg.Metadata(); metaErr == nil && metadata.NumDelivered >= 5 {
			_ = w.publishDLQ(ctx, msg, err)
			_ = msg.Ack()
			return
		}
		_ = msg.NakWithDelay(backoffDelay(msg))
		return
	}
	_ = msg.Ack()
}

func (w *Worker) publishDLQ(ctx context.Context, msg *natsgo.Msg, err error) error {
	payload := map[string]any{
		"subject":    msg.Subject,
		"error":      err.Error(),
		"payload":    json.RawMessage(msg.Data),
		"created_at": time.Now().UTC(),
	}
	return w.client.PublishJSON(ctx, SubjectDLQ, payload)
}

func backoffDelay(msg *natsgo.Msg) time.Duration {
	metadata, err := msg.Metadata()
	if err != nil {
		return 10 * time.Second
	}
	switch metadata.NumDelivered {
	case 1:
		return 10 * time.Second
	case 2:
		return 30 * time.Second
	case 3:
		return 2 * time.Minute
	case 4:
		return 5 * time.Minute
	default:
		return 15 * time.Minute
	}
}

func envelopeJobID(body []byte) string {
	var envelope struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return envelope.JobID
}

func DecodeEnvelope[T any](body []byte) (models.Envelope[T], error) {
	var envelope models.Envelope[T]
	if err := json.Unmarshal(body, &envelope); err != nil {
		return envelope, fmt.Errorf("decode envelope: %w", err)
	}
	return envelope, nil
}
