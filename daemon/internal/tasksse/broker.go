// Package tasksse provides the bounded process-local replay stream for global
// Generation and Export task notifications. Durable task state stays in each
// portable project database.
package tasksse

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

	project "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const (
	MaxEvents          = 2_000
	ReplayWindow       = 5 * time.Minute
	subscriberCapacity = 256
)

var ErrInvalidBroker = errors.New("invalid Task event broker configuration")

const (
	EventGenerationJobUpdated = "generation.job.updated"
	EventExportJobUpdated     = "export.job.updated"
	EventTasksResyncRequired  = "tasks.resync_required"
)

type Clock interface{ Now() time.Time }

type Event struct {
	ID         string
	Type       string
	Job        *project.GenerationJob
	ExportJob  *project.ExportJob
	Summary    string
	OccurredAt time.Time
}

// PublishExportJob implements projectapp.ExportEventPublisher.
func (b *Broker) PublishExportJob(value project.ExportJob) {
	if b == nil || value.Validate() != nil {
		return
	}
	job := value
	b.publish(Event{Type: EventExportJobUpdated, ExportJob: &job, OccurredAt: value.UpdatedAt})
}

type storedEvent struct {
	sequence uint64
	event    Event
}

type subscriber struct{ events chan Event }

type Broker struct {
	mutex       sync.Mutex
	epoch       string
	clock       Clock
	sequence    uint64
	events      []storedEvent
	subscribers map[*subscriber]struct{}
}

func NewBroker(clock Clock, randomSource io.Reader) (*Broker, error) {
	if clock == nil {
		return nil, ErrInvalidBroker
	}
	if randomSource == nil {
		randomSource = rand.Reader
	}
	buffer := make([]byte, 16)
	if _, err := io.ReadFull(randomSource, buffer); err != nil {
		return nil, fmt.Errorf("create Task event epoch: %w", err)
	}
	return &Broker{epoch: base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer), clock: clock, subscribers: make(map[*subscriber]struct{})}, nil
}

// PublishGenerationJob implements projectapp.GenerationEventPublisher.
func (b *Broker) PublishGenerationJob(value project.GenerationJob) {
	if b == nil || value.Validate() != nil {
		return
	}
	job := value
	b.publish(Event{Type: EventGenerationJobUpdated, Job: &job, OccurredAt: value.UpdatedAt})
}

func (b *Broker) Cursor() (string, error) {
	if b == nil {
		return "", ErrInvalidBroker
	}
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.prune(b.clock.Now())
	return b.cursor(b.sequence), nil
}

func (b *Broker) StreamEpoch() (string, error) {
	if b == nil {
		return "", ErrInvalidBroker
	}
	return b.epoch, nil
}

// Capture serializes an authoritative SQLite snapshot with the replay cursor.
// Durable writes may proceed concurrently, but their Publish call cannot pass
// this boundary until the returned cursor has been fixed.
func (b *Broker) Capture(read func() error) (string, error) {
	if b == nil || read == nil {
		return "", ErrInvalidBroker
	}
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.prune(b.clock.Now())
	if err := read(); err != nil {
		return "", err
	}
	return b.cursor(b.sequence), nil
}

func (b *Broker) Subscribe(ctx context.Context, after string) (<-chan Event, func(), error) {
	if b == nil || ctx == nil {
		return nil, nil, ErrInvalidBroker
	}
	b.mutex.Lock()
	b.prune(b.clock.Now())
	sequence, valid := b.parseCursor(after)
	if after == "" {
		sequence, valid = b.sequence, true
	}
	oldestAvailable := b.sequence + 1
	if len(b.events) > 0 {
		oldestAvailable = b.events[0].sequence
	}
	if !valid || sequence > b.sequence || sequence+1 < oldestAvailable {
		result := make(chan Event, 1)
		result <- Event{ID: b.cursor(b.sequence), Type: EventTasksResyncRequired, Summary: "event cursor cannot be replayed", OccurredAt: b.clock.Now()}
		close(result)
		b.mutex.Unlock()
		return result, func() {}, nil
	}
	replayCount := int(b.sequence - sequence)
	capacity := subscriberCapacity
	if replayCount+1 > capacity {
		capacity = replayCount + 1
	}
	target := &subscriber{events: make(chan Event, capacity)}
	for _, item := range b.events {
		if item.sequence > sequence {
			target.events <- item.event
		}
	}
	b.subscribers[target] = struct{}{}
	b.mutex.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mutex.Lock()
			if _, exists := b.subscribers[target]; exists {
				delete(b.subscribers, target)
				close(target.events)
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

func (b *Broker) publish(value Event) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.prune(b.clock.Now())
	b.sequence++
	value.ID = b.cursor(b.sequence)
	b.events = append(b.events, storedEvent{sequence: b.sequence, event: value})
	if len(b.events) > MaxEvents {
		b.events = append([]storedEvent(nil), b.events[len(b.events)-MaxEvents:]...)
	}
	for target := range b.subscribers {
		select {
		case target.events <- value:
		default:
			delete(b.subscribers, target)
			for len(target.events) > 0 {
				<-target.events
			}
			target.events <- Event{ID: b.cursor(b.sequence), Type: EventTasksResyncRequired, Summary: "event consumer fell behind", OccurredAt: b.clock.Now()}
			close(target.events)
		}
	}
}

func (b *Broker) prune(now time.Time) {
	cutoff := now.Add(-ReplayWindow)
	first := 0
	for first < len(b.events) && b.events[first].event.OccurredAt.Before(cutoff) {
		first++
	}
	if first > 0 {
		b.events = append([]storedEvent(nil), b.events[first:]...)
	}
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
