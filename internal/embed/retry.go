package embed

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

type NonRetryableError struct{ Err error }

func (e *NonRetryableError) Error() string { return e.Err.Error() }
func (e *NonRetryableError) Unwrap() error { return e.Err }

type retryProvider struct {
	inner    Provider
	attempts int
}

func WithRetry(p Provider, attempts int) Provider {
	return &retryProvider{inner: p, attempts: attempts}
}

func (r *retryProvider) Name() string   { return r.inner.Name() }
func (r *retryProvider) Model() string  { return r.inner.Model() }
func (r *retryProvider) Dimension() int { return r.inner.Dimension() }

func (r *retryProvider) Embed(ctx context.Context, t string) ([]float32, error) {
	var v []float32
	err := doRetry(ctx, r.attempts, func() error {
		out, err := r.inner.Embed(ctx, t)
		v = out
		return err
	})
	return v, err
}

func (r *retryProvider) EmbedBatch(ctx context.Context, ts []string) ([][]float32, error) {
	var v [][]float32
	err := doRetry(ctx, r.attempts, func() error {
		out, err := r.inner.EmbedBatch(ctx, ts)
		v = out
		return err
	})
	return v, err
}

func doRetry(ctx context.Context, attempts int, fn func() error) error {
	var last error
	backoff := 500 * time.Millisecond
	for i := 0; i < attempts; i++ {
		err := fn()
		if err == nil {
			return nil
		}
		var nr *NonRetryableError
		if errors.As(err, &nr) {
			return err
		}
		last = err
		if i == attempts-1 {
			break
		}
		jitter := time.Duration(rand.Int63n(int64(backoff) / 2))
		select {
		case <-time.After(backoff + jitter):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff *= 2
		if backoff > 10*time.Second {
			backoff = 10 * time.Second
		}
	}
	return last
}
