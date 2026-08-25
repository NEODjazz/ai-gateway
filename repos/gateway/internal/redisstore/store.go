package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
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
	if s == nil {
		return nil, false, errors.New("redis store is not configured")
	}
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
	if s == nil {
		return errors.New("redis store is not configured")
	}
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

var circuitPermitScript = redis.NewScript(`
local open_until = redis.call('GET', KEYS[1])
if not open_until then return 1 end
if tonumber(open_until) > tonumber(ARGV[1]) then return 0 end
local acquired = redis.call('SET', KEYS[2], '1', 'NX', 'PX', ARGV[2])
if acquired then return 1 end
return 0
`)

var circuitFailureScript = redis.NewScript(`
local open_until = redis.call('GET', KEYS[1])
if open_until then
  redis.call('SET', KEYS[1], tonumber(ARGV[1]) + tonumber(ARGV[3]))
  redis.call('DEL', KEYS[2], KEYS[3])
  return 1
end
local failures = redis.call('INCR', KEYS[3])
if failures == 1 then redis.call('PEXPIRE', KEYS[3], ARGV[4]) end
if failures >= tonumber(ARGV[2]) then
  redis.call('SET', KEYS[1], tonumber(ARGV[1]) + tonumber(ARGV[3]))
  redis.call('DEL', KEYS[2], KEYS[3])
  return 1
end
return 0
`)

func (s *Store) CircuitAvailable(ctx context.Context, endpoint string, now time.Time) (bool, error) {
	if s == nil {
		return false, errors.New("redis store is not configured")
	}
	stateKey, _, _ := s.circuitKeys(endpoint)
	value, err := s.client.Get(ctx, stateKey).Result()
	if errors.Is(err, redis.Nil) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	openUntil, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return false, errors.New("invalid redis circuit state")
	}
	return now.UnixMilli() >= openUntil, nil
}

func (s *Store) CircuitPermit(ctx context.Context, endpoint string, now time.Time, probeTTL time.Duration) (bool, error) {
	if s == nil {
		return false, errors.New("redis store is not configured")
	}
	if probeTTL <= 0 {
		probeTTL = 30 * time.Second
	}
	stateKey, probeKey, _ := s.circuitKeys(endpoint)
	result, err := circuitPermitScript.Run(ctx, s.client, []string{stateKey, probeKey}, now.UnixMilli(), probeTTL.Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (s *Store) CircuitSuccess(ctx context.Context, endpoint string) error {
	if s == nil {
		return errors.New("redis store is not configured")
	}
	stateKey, probeKey, failuresKey := s.circuitKeys(endpoint)
	return s.client.Del(ctx, stateKey, probeKey, failuresKey).Err()
}

func (s *Store) CircuitFailure(ctx context.Context, endpoint string, threshold int, cooldown time.Duration, now time.Time) error {
	if s == nil {
		return errors.New("redis store is not configured")
	}
	if threshold <= 0 {
		threshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	failureWindow := max(cooldown, time.Minute)
	stateKey, probeKey, failuresKey := s.circuitKeys(endpoint)
	return circuitFailureScript.Run(ctx, s.client, []string{stateKey, probeKey, failuresKey}, now.UnixMilli(), threshold, cooldown.Milliseconds(), failureWindow.Milliseconds()).Err()
}

func (s *Store) cacheKey(key string) string {
	return s.prefix + ":cache:" + key
}

func (s *Store) circuitKeys(endpoint string) (state, probe, failures string) {
	endpointHash := sha256.Sum256([]byte(endpoint))
	base := s.prefix + ":circuit:" + hex.EncodeToString(endpointHash[:16])
	return base + ":open-until", base + ":probe", base + ":failures"
}
