package filestate

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("file not found")
var ErrQuotaExceeded = errors.New("file storage quota exceeded")
var ErrConflict = errors.New("file already exists")
var ErrUnavailable = errors.New("file storage is unavailable")
var ErrInvalid = errors.New("invalid file storage request")

type File struct {
	ID          string    `json:"id"`
	OwnerKey    string    `json:"-"`
	Filename    string    `json:"filename"`
	Purpose     string    `json:"purpose"`
	ContentType string    `json:"content_type"`
	Bytes       int64     `json:"bytes"`
	CreatedAt   time.Time `json:"-"`
	Content     []byte    `json:"-"`
}

type Store interface {
	Create(context.Context, File, int64) (File, error)
	List(context.Context, string, string, int, string) ([]File, string, error)
	Get(context.Context, string, string, bool) (File, error)
	Delete(context.Context, string, string) error
}
