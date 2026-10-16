package realtimestate

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	MaxTickets      = 1024
	MaxOwnerTickets = 8
	MaxPayloadBytes = 16 << 10
	TicketTTL       = 30 * time.Second
)

var (
	ErrInvalid     = errors.New("invalid realtime browser ticket")
	ErrNotFound    = errors.New("realtime browser ticket is absent or expired")
	ErrCapacity    = errors.New("realtime browser ticket capacity exceeded")
	ErrConflict    = errors.New("realtime browser ticket already exists")
	ErrUnavailable = errors.New("realtime browser ticket storage is unavailable")
)

type Ticket struct {
	Hash      string
	OwnerKey  string
	Model     string
	Origin    string
	Payload   []byte
	ExpiresAt time.Time
}

type Store interface {
	CreateRealtimeTicket(context.Context, Ticket) error
	ConsumeRealtimeTicket(context.Context, string, string, string) (Ticket, error)
}

func Valid(ticket Ticket, now time.Time) bool {
	return validHash(ticket.Hash) && validHash(ticket.OwnerKey) && ticket.Model != "" && len(ticket.Model) <= 256 && ticket.Origin != "" && len(ticket.Origin) <= 2048 && len(ticket.Payload) > 0 && len(ticket.Payload) <= MaxPayloadBytes && ticket.ExpiresAt.After(now) && !ticket.ExpiresAt.After(now.Add(TicketTTL))
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

type MemoryStore struct {
	mu                   sync.Mutex
	tickets              map[string]Ticket
	capacity, ownerQuota int
	now                  func() time.Time
}

func NewMemoryStore(capacity, ownerQuota int) *MemoryStore {
	if capacity < 1 || capacity > MaxTickets {
		capacity = MaxTickets
	}
	if ownerQuota < 1 || ownerQuota > MaxOwnerTickets {
		ownerQuota = MaxOwnerTickets
	}
	return &MemoryStore{tickets: make(map[string]Ticket), capacity: capacity, ownerQuota: ownerQuota, now: time.Now}
}

func (s *MemoryStore) CreateRealtimeTicket(ctx context.Context, ticket Ticket) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	now := s.now()
	if !Valid(ticket, now) {
		return ErrInvalid
	}
	ownerCount := 0
	for hash, item := range s.tickets {
		if !item.ExpiresAt.After(now) {
			delete(s.tickets, hash)
			continue
		}
		if item.OwnerKey == ticket.OwnerKey {
			ownerCount++
		}
	}
	if _, exists := s.tickets[ticket.Hash]; exists {
		return ErrConflict
	}
	if len(s.tickets) >= s.capacity || ownerCount >= s.ownerQuota {
		return ErrCapacity
	}
	ticket.Payload = append([]byte(nil), ticket.Payload...)
	s.tickets[ticket.Hash] = ticket
	return nil
}

func (s *MemoryStore) ConsumeRealtimeTicket(ctx context.Context, hash, model, origin string) (Ticket, error) {
	if err := ctx.Err(); err != nil {
		return Ticket{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Ticket{}, err
	}
	item, exists := s.tickets[hash]
	if !exists {
		return Ticket{}, ErrNotFound
	}
	if !item.ExpiresAt.After(s.now()) {
		delete(s.tickets, hash)
		return Ticket{}, ErrNotFound
	}
	if item.Model != model || item.Origin != origin {
		return Ticket{}, ErrNotFound
	}
	delete(s.tickets, hash)
	item.Payload = append([]byte(nil), item.Payload...)
	return item, nil
}
