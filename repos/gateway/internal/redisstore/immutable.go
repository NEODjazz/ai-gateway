package redisstore

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var setIfAbsentOrEqualScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current then
  if current == ARGV[1] then return 1 end
  return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return 1
`)

var deleteIfEqualScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current then return 1 end
if current ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1])
return 1
`)

// SetIfAbsentOrEqual atomically creates a record or accepts an identical retry.
// An existing record is never overwritten and its expiration is not extended.
func (s *Store) SetIfAbsentOrEqual(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if s == nil {
		return false, errors.New("redis store is not configured")
	}
	if key == "" || len(value) == 0 || len(value) > 4096 || ttl < time.Millisecond {
		return false, errors.New("invalid immutable record")
	}
	result, err := setIfAbsentOrEqualScript.Run(ctx, s.client, []string{s.cacheKey(key)}, value, ttl.Milliseconds()).Int()
	return result == 1, err
}

// DeleteIfEqual atomically removes an immutable record only when its current
// value still matches the caller's observed binding. A missing record is an
// idempotent success.
func (s *Store) DeleteIfEqual(ctx context.Context, key string, value []byte) (bool, error) {
	if s == nil {
		return false, errors.New("redis store is not configured")
	}
	if key == "" || len(value) == 0 || len(value) > 4096 {
		return false, errors.New("invalid immutable record")
	}
	result, err := deleteIfEqualScript.Run(ctx, s.client, []string{s.cacheKey(key)}, value).Int()
	return result == 1, err
}
