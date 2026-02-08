package log_test

import (
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.jacobcolvin.com/x/log"
)

func TestNewPublisher(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		opts    []log.PublisherOption
		wantCap int
	}{
		"default buffer size": {
			opts:    nil,
			wantCap: 64,
		},
		"custom buffer size": {
			opts:    []log.PublisherOption{log.WithBufferSize(128)},
			wantCap: 128,
		},
		"clamp zero to one": {
			opts:    []log.PublisherOption{log.WithBufferSize(0)},
			wantCap: 1,
		},
		"clamp negative to one": {
			opts:    []log.PublisherOption{log.WithBufferSize(-5)},
			wantCap: 1,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			pub := log.NewPublisher(tc.opts...)

			sub := pub.Subscribe()
			defer sub.Close()

			assert.Equal(t, tc.wantCap, cap(sub.C()))
		})
	}
}

func TestPublisherWrite(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		numSubscribers int
		want           string
	}{
		"single subscriber": {
			numSubscribers: 1,
			want:           "hello",
		},
		"multiple subscribers": {
			numSubscribers: 3,
			want:           "hello",
		},
		"no subscribers": {
			numSubscribers: 0,
			want:           "",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			pub := log.NewPublisher()

			subs := make([]*log.Subscription, tc.numSubscribers)
			for i := range subs {
				subs[i] = pub.Subscribe()
			}

			n, err := pub.Write([]byte("hello"))
			require.NoError(t, err)
			assert.Equal(t, 5, n)

			for _, sub := range subs {
				got := <-sub.C()
				assert.Equal(t, tc.want, string(got))
			}
		})
	}

	t.Run("write copies input", func(t *testing.T) {
		t.Parallel()

		pub := log.NewPublisher()
		sub := pub.Subscribe()

		buf := []byte("original")
		_, err := pub.Write(buf)
		require.NoError(t, err)

		// Mutate the original buffer.
		buf[0] = 'X'

		got := <-sub.C()
		assert.Equal(t, "original", string(got), "subscriber should receive a copy, not the original slice")
	})
}

func TestPublisherRingBuffer(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		bufSize int
		writes  []string
		want    []string
	}{
		"drops oldest on full": {
			bufSize: 2,
			writes:  []string{"a", "b", "c", "d"},
			want:    []string{"c", "d"},
		},
		"preserves newest entries": {
			bufSize: 3,
			writes:  []string{"1", "2", "3", "4", "5"},
			want:    []string{"3", "4", "5"},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			pub := log.NewPublisher(log.WithBufferSize(tc.bufSize))
			sub := pub.Subscribe()

			for _, w := range tc.writes {
				_, err := pub.Write([]byte(w))
				require.NoError(t, err)
			}

			var got []string
			for range tc.want {
				got = append(got, string(<-sub.C()))
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSubscriptionClose(t *testing.T) {
	t.Parallel()

	t.Run("stops delivery", func(t *testing.T) {
		t.Parallel()

		pub := log.NewPublisher()
		sub := pub.Subscribe()

		_, err := pub.Write([]byte("before"))
		require.NoError(t, err)

		sub.Close()

		// Trigger compaction.
		_, err = pub.Write([]byte("after"))
		require.NoError(t, err)

		// "before" was buffered prior to close; "after" should not appear.
		got := <-sub.C()
		assert.Equal(t, "before", string(got))

		// Channel should now be closed.
		_, open := <-sub.C()
		assert.False(t, open, "channel should be closed after subscription close + compaction")
	})

	t.Run("idempotent", func(t *testing.T) {
		t.Parallel()

		pub := log.NewPublisher()
		sub := pub.Subscribe()

		sub.Close()
		sub.Close() // should not panic
		sub.Close()

		// Trigger compaction to close channel.
		_, err := pub.Write([]byte("x"))
		require.NoError(t, err)

		_, open := <-sub.C()
		assert.False(t, open)
	})
}

func TestPublisherClose(t *testing.T) {
	t.Parallel()

	t.Run("closes all subscriptions", func(t *testing.T) {
		t.Parallel()

		pub := log.NewPublisher()
		sub1 := pub.Subscribe()
		sub2 := pub.Subscribe()

		require.NoError(t, pub.Close())

		_, open1 := <-sub1.C()
		_, open2 := <-sub2.C()

		assert.False(t, open1)
		assert.False(t, open2)
	})

	t.Run("write after close is no-op", func(t *testing.T) {
		t.Parallel()

		pub := log.NewPublisher()
		sub := pub.Subscribe()

		require.NoError(t, pub.Close())

		n, err := pub.Write([]byte("ignored"))
		require.NoError(t, err)
		assert.Equal(t, 7, n)

		_, open := <-sub.C()
		assert.False(t, open)
	})

	t.Run("idempotent", func(t *testing.T) {
		t.Parallel()

		pub := log.NewPublisher()
		require.NoError(t, pub.Close())
		require.NoError(t, pub.Close())
	})

	t.Run("subscribe after close", func(t *testing.T) {
		t.Parallel()

		pub := log.NewPublisher()
		require.NoError(t, pub.Close())

		sub := pub.Subscribe()
		_, open := <-sub.C()
		assert.False(t, open, "subscription from closed publisher should have closed channel")
	})
}

func TestPublisherConcurrency(t *testing.T) {
	t.Parallel()

	pub := log.NewPublisher(log.WithBufferSize(8))

	var wg sync.WaitGroup

	// Concurrent writers.
	for range 5 {
		wg.Go(func() {
			for range 100 {
				//nolint:errcheck // Write always returns nil; checking would complicate goroutine.
				pub.Write([]byte("data"))
			}
		})
	}

	// Concurrent subscribers.
	for range 5 {
		wg.Go(func() {
			sub := pub.Subscribe()
			for range 20 {
				select {
				case <-sub.C():
				default:
				}
			}

			sub.Close()
		})
	}

	wg.Wait()
	require.NoError(t, pub.Close())
}

func TestPublisherWithHandler(t *testing.T) {
	t.Parallel()

	pub := log.NewPublisher()
	t.Cleanup(func() { require.NoError(t, pub.Close()) })

	sub := pub.Subscribe()

	handler := log.NewHandler(pub, log.LevelInfo, log.FormatJSON)
	logger := slog.New(handler)

	logger.Info("hello from publisher", slog.String("key", "value"))

	entry := <-sub.C()
	got := string(entry)
	assert.Contains(t, got, "hello from publisher")
	assert.Contains(t, got, `"key":"value"`)
}
