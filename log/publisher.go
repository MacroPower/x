package log

import (
	"sync"
	"sync/atomic"
)

const defaultBufferSize = 64

// Publisher is an [io.Writer] that fans out written bytes to subscribers.
//
// Each call to [Publisher.Write] copies the input once and delivers it to
// every active [Subscription] via a buffered channel with ring-buffer
// semantics: when a subscriber's channel is full the oldest entry is dropped
// so Write never blocks. Safe for concurrent use.
//
// Create instances with [NewPublisher].
type Publisher struct {
	subscribers []*Subscription
	bufSize     int
	mu          sync.Mutex
	closed      bool
}

// NewPublisher creates a [Publisher] with the given options.
// The default buffer size is 64.
func NewPublisher(opts ...PublisherOption) *Publisher {
	p := &Publisher{
		bufSize: defaultBufferSize,
	}
	for _, opt := range opts {
		opt(p)
	}

	return p
}

// PublisherOption configures a [Publisher].
type PublisherOption func(*Publisher)

// WithBufferSize sets the channel buffer size for new subscriptions.
// Values less than 1 are clamped to 1.
func WithBufferSize(n int) PublisherOption {
	return func(p *Publisher) {
		if n < 1 {
			n = 1
		}

		p.bufSize = n
	}
}

// Write copies b and sends the copy to all active subscribers. When a
// subscriber's channel is full the oldest entry is dropped to make room.
// Closed subscriptions are compacted out of the subscriber list. Write
// always returns len(b), nil.
func (p *Publisher) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return len(b), nil
	}

	entry := make([]byte, len(b))
	copy(entry, b)

	// Compact closed subscriptions and deliver in one pass.
	alive := p.subscribers[:0]
	for _, sub := range p.subscribers {
		if sub.closed.Load() {
			close(sub.ch)
			continue
		}
		// Ring-buffer: drop oldest if full.
		select {
		case sub.ch <- entry:
		default:
			<-sub.ch

			sub.ch <- entry
		}

		alive = append(alive, sub)
	}
	// Clear trailing references for GC.
	for i := len(alive); i < len(p.subscribers); i++ {
		p.subscribers[i] = nil
	}

	p.subscribers = alive

	return len(b), nil
}

// Subscribe creates and registers a new [Subscription]. If the Publisher is
// already closed the returned subscription's channel is immediately closed.
func (p *Publisher) Subscribe() *Subscription {
	p.mu.Lock()
	defer p.mu.Unlock()

	sub := &Subscription{
		ch: make(chan []byte, p.bufSize),
	}

	if p.closed {
		close(sub.ch)
		return sub
	}

	p.subscribers = append(p.subscribers, sub)

	return sub
}

// Close marks the Publisher as closed, closes all subscription channels,
// and releases the subscriber list. Idempotent.
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.closed = true
	for _, sub := range p.subscribers {
		close(sub.ch)
	}

	p.subscribers = nil

	return nil
}

// Subscription receives log entries from a [Publisher].
type Subscription struct {
	ch     chan []byte
	closed atomic.Bool
}

// C returns the read-only channel that delivers log entries.
// Callers must not modify the returned byte slices.
func (s *Subscription) C() <-chan []byte {
	return s.ch
}

// Close marks the subscription as closed. The Publisher will close the
// underlying channel on its next Write or Close call. Idempotent.
func (s *Subscription) Close() {
	s.closed.Store(true)
}
