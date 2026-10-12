// Package agentsse provides the bounded, process-local replay log used by the
// Agent Session SSE transport. Session and project facts remain in SQLite;
// this buffer contains notifications only.
package agentsse

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	agent "github.com/shotforward/shotforward/daemon/internal/domain/agent"
)

const (
	MaxEventsPerSession = 2_000
	ReplayWindow        = 5 * time.Minute
	subscriberCapacity  = 256
)

var (
	ErrInvalidBroker = errors.New("invalid Agent event broker configuration")
	ErrInvalidCursor = errors.New("invalid Agent event cursor")
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

type Event struct {
	ID    string
	Value agentapp.StudioEvent
}

type storedEvent struct {
	sequence uint64
	value    agentapp.StudioEvent
}

type subscriber struct{ events chan Event }

type sessionStream struct {
	sequence    uint64
	events      []storedEvent
	subscribers map[*subscriber]struct{}
}

type Broker struct {
	mutex   sync.Mutex
	epoch   string
	clock   Clock
	streams map[agent.SessionID]*sessionStream
}

func NewBroker(clock Clock, random io.Reader) (*Broker, error) {
	if clock == nil {
		clock = systemClock{}
	}
	if random == nil {
		random = rand.Reader
	}
	buffer := make([]byte, 16)
	if _, err := io.ReadFull(random, buffer); err != nil {
		return nil, fmt.Errorf("create Agent event epoch: %w", err)
	}
	epoch := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer)
	return &Broker{epoch: epoch, clock: clock, streams: make(map[agent.SessionID]*sessionStream)}, nil
}

// Publish implements agentapp.EventPublisher. Invalid events are discarded at
// this trust boundary instead of contaminating the replay stream.
func (b *Broker) Publish(value agentapp.StudioEvent) {
	if b == nil || value.Validate() != nil {
		return
	}
	b.mutex.Lock()
	defer b.mutex.Unlock()
	stream := b.stream(value.SessionID)
	b.prune(stream, b.clock.Now())
	stream.sequence++
	stored := storedEvent{sequence: stream.sequence, value: value}
	stream.events = append(stream.events, stored)
	if len(stream.events) > MaxEventsPerSession {
		stream.events = append([]storedEvent(nil), stream.events[len(stream.events)-MaxEventsPerSession:]...)
	}
	recorded := Event{ID: b.cursor(stream.sequence), Value: value}
	for target := range stream.subscribers {
		select {
		case target.events <- recorded:
		default:
			delete(stream.subscribers, target)
			b.replaceWithResync(target, value.SessionID, stream.sequence, "event consumer fell behind")
		}
	}
}

func (b *Broker) Cursor(sessionID agent.SessionID) (string, error) {
	if b == nil || sessionID.Validate() != nil {
		return "", ErrInvalidBroker
	}
	b.mutex.Lock()
	defer b.mutex.Unlock()
	stream := b.stream(sessionID)
	b.prune(stream, b.clock.Now())
	return b.cursor(stream.sequence), nil
}

// Capture serializes an authoritative session snapshot with its replay cursor.
// A durable Turn update may complete concurrently, but its Publish call cannot
// pass this boundary until the cursor paired with the snapshot has been fixed.
func (b *Broker) Capture(sessionID agent.SessionID, read func() error) (string, error) {
	if b == nil || sessionID.Validate() != nil || read == nil {
		return "", ErrInvalidBroker
	}
	b.mutex.Lock()
	defer b.mutex.Unlock()
	stream := b.stream(sessionID)
	b.prune(stream, b.clock.Now())
	if err := read(); err != nil {
		return "", err
	}
	return b.cursor(stream.sequence), nil
}

// Subscribe atomically captures replay and registers for future events. A bad,
// expired, future, or prior-process cursor yields one resync event and closes.
func (b *Broker) Subscribe(ctx context.Context, sessionID agent.SessionID, after string) (<-chan Event, func(), error) {
	if b == nil || ctx == nil || sessionID.Validate() != nil {
		return nil, nil, ErrInvalidBroker
	}
	b.mutex.Lock()
	stream := b.stream(sessionID)
	b.prune(stream, b.clock.Now())
	sequence, valid := b.parseCursor(after)
	if after == "" {
		sequence, valid = stream.sequence, true
	}
	oldestAvailable := stream.sequence + 1
	if len(stream.events) > 0 {
		oldestAvailable = stream.events[0].sequence
	}
	if !valid || sequence > stream.sequence || sequence+1 < oldestAvailable {
		result := make(chan Event, 1)
		target := &subscriber{events: result}
		b.replaceWithResync(target, sessionID, stream.sequence, "event cursor cannot be replayed")
		b.mutex.Unlock()
		return result, func() {}, nil
	}
	replayCount := int(stream.sequence - sequence)
	capacity := subscriberCapacity
	if replayCount+1 > capacity {
		capacity = replayCount + 1
	}
	target := &subscriber{events: make(chan Event, capacity)}
	for _, item := range stream.events {
		if item.sequence > sequence {
			target.events <- Event{ID: b.cursor(item.sequence), Value: item.value}
		}
	}
	stream.subscribers[target] = struct{}{}
	b.mutex.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mutex.Lock()
			if active := b.streams[sessionID]; active != nil {
				if _, ok := active.subscribers[target]; ok {
					delete(active.subscribers, target)
					close(target.events)
				}
			}
			b.mutex.Unlock()
		})
	}
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return target.events, cancel, nil
}

func (b *Broker) stream(sessionID agent.SessionID) *sessionStream {
	stream := b.streams[sessionID]
	if stream == nil {
		stream = &sessionStream{subscribers: make(map[*subscriber]struct{})}
		b.streams[sessionID] = stream
	}
	return stream
}

func (b *Broker) prune(stream *sessionStream, now time.Time) {
	cutoff := now.Add(-ReplayWindow)
	first := 0
	for first < len(stream.events) && stream.events[first].value.OccurredAt.Before(cutoff) {
		first++
	}
	if first > 0 {
		stream.events = append([]storedEvent(nil), stream.events[first:]...)
	}
}

func (b *Broker) replaceWithResync(target *subscriber, sessionID agent.SessionID, sequence uint64, summary string) {
	for len(target.events) > 0 {
		<-target.events
	}
	target.events <- Event{ID: b.cursor(sequence), Value: agentapp.StudioEvent{
		Type: agentapp.EventSessionResyncRequired, SessionID: sessionID,
		Summary: summary, OccurredAt: b.clock.Now(),
	}}
	close(target.events)
}

func (b *Broker) cursor(sequence uint64) string {
	return b.epoch + ":" + strconv.FormatUint(sequence, 10)
}

func (b *Broker) parseCursor(value string) (uint64, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || parts[0] != b.epoch || parts[1] == "" {
		return 0, false
	}
	sequence, err := strconv.ParseUint(parts[1], 10, 64)
	return sequence, err == nil
}
