package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

type Config struct {
	Addr     string
	Password string
	DB       int
	Prefix   string
}

type Store struct {
	client redis.UniversalClient
	prefix string
}

func New(cfg Config) *Store {
	if cfg.Addr == "" {
		return nil
	}
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "ai-gateway"
	}
	return &Store{
		client: redis.NewClient(&redis.Options{Addr: cfg.Addr, Password: cfg.Password, DB: cfg.DB}),
		prefix: prefix,
	}
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil {
		return errors.New("redis store is not configured")
	}
	return s.client.Ping(ctx).Err()
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, err := s.client.Get(ctx, s.cacheKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func (s *Store) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.client.Set(ctx, s.cacheKey(key), value, ttl).Err()
}

var fixedWindowScript = redis.NewScript(`
local current_requests = tonumber(redis.call('GET', KEYS[1]) or '0')
local current_tokens = tonumber(redis.call('GET', KEYS[2]) or '0')
local request_limit = tonumber(ARGV[1])
local token_limit = tonumber(ARGV[2])
local requested_tokens = tonumber(ARGV[3])
local window_ms = tonumber(ARGV[4])

if (request_limit > 0 and current_requests + 1 > request_limit) or
   (token_limit > 0 and current_tokens + requested_tokens > token_limit) then
  local ttl = redis.call('PTTL', KEYS[1])
  if ttl < 0 then ttl = redis.call('PTTL', KEYS[2]) end
  if ttl < 0 then ttl = window_ms end
  return {0, ttl}
end

local requests = redis.call('INCR', KEYS[1])
redis.call('INCRBY', KEYS[2], requested_tokens)
if requests == 1 then
  redis.call('PEXPIRE', KEYS[1], window_ms)
  redis.call('PEXPIRE', KEYS[2], window_ms)
end
return {1, redis.call('PTTL', KEYS[1])}
`)

func (s *Store) Allow(ctx context.Context, identity string, requestLimit, tokenLimit, tokens int, window time.Duration) (bool, time.Duration, error) {
	if s == nil {
		return false, 0, errors.New("redis store is not configured")
	}
	if requestLimit <= 0 && tokenLimit <= 0 {
		return true, 0, nil
	}
	if tokens < 0 {
		tokens = 0
	}
	if window <= 0 {
		window = time.Minute
	}
	identityHash := sha256.Sum256([]byte(identity))
	base := s.prefix + ":rate:" + hex.EncodeToString(identityHash[:16])
	result, err := fixedWindowScript.Run(ctx, s.client, []string{base + ":requests", base + ":tokens"}, requestLimit, tokenLimit, tokens, window.Milliseconds()).Slice()
	if err != nil {
		return false, 0, err
	}
	if len(result) != 2 {
		return false, 0, errors.New("unexpected redis rate-limit response")
	}
	allowed, ok := result[0].(int64)
	if !ok {
		return false, 0, errors.New("invalid redis rate-limit decision")
	}
	ttlMS, ok := result[1].(int64)
	if !ok {
		return false, 0, errors.New("invalid redis rate-limit ttl")
	}
	return allowed == 1, time.Duration(ttlMS) * time.Millisecond, nil
}

func (s *Store) cacheKey(key string) string {
	return s.prefix + ":cache:" + key
}
