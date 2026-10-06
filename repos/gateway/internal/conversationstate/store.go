package conversationstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

var ErrNotFound = errors.New("conversation not found")
var ErrQuotaExceeded = errors.New("conversation quota exceeded")
var ErrConflict = errors.New("conversation state conflict")
var ErrUnavailable = errors.New("conversation storage is unavailable")
var ErrInvalid = errors.New("invalid conversation record")

const MaxMetadataBytes = 64 << 10
const MaxItemBytes = 2 << 20

type Conversation struct {
	ID        string
	OwnerKey  string
	Metadata  []byte
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Item struct {
	ID             string
	ConversationID string
	OwnerKey       string
	Payload        []byte
	Ordinal        int64
	CreatedAt      time.Time
}

type PageOptions struct {
	Limit int
	After string
	Order string
}

type Turn struct {
	Conversation Conversation
	Items        []Item
	ExecutionID  string
	Durable      bool
}

type Store interface {
	CreateConversation(context.Context, Conversation, []Item, int, int) (Conversation, []Item, error)
	GetConversation(context.Context, string, string) (Conversation, error)
	UpdateConversation(context.Context, string, string, []byte, int64) (Conversation, error)
	DeleteConversation(context.Context, string, string) error
	CreateItems(context.Context, string, string, []Item, int) ([]Item, error)
	ListItems(context.Context, string, string, PageOptions) ([]Item, bool, error)
	GetItem(context.Context, string, string, string) (Item, error)
	DeleteItem(context.Context, string, string, string) error
	BeginTurn(context.Context, string, string, string, time.Duration) (Turn, error)
	StageTurn(context.Context, Turn, []Item, int, int) error
	CompleteTurn(context.Context, Turn, []Item, int) error
	ReleaseTurn(context.Context, Turn) error
}

func OwnerKey(credentialID, userID string) string {
	digest := sha256.Sum256([]byte(credentialID + "\x00" + userID))
	return "v1/" + hex.EncodeToString(digest[:])
}
