package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
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

var pushBoundedScript = redis.NewScript(`
redis.call('LPUSH', KEYS[1], ARGV[1])
redis.call('LTRIM', KEYS[1], 0, tonumber(ARGV[2]) - 1)
if tonumber(ARGV[3]) > 0 then redis.call('PEXPIRE', KEYS[1], ARGV[3]) end
return redis.call('LLEN', KEYS[1])
`)

// PushBounded prepends an opaque value to a namespaced bounded list. The
// namespace is hashed before it becomes a Redis key so caller-controlled
// labels cannot increase key cardinality or leak metadata.
func (s *Store) PushBounded(ctx context.Context, namespace string, value []byte, limit int, ttl time.Duration) error {
	if s == nil {
		return errors.New("redis store is not configured")
	}
	if namespace == "" || len(value) == 0 || len(value) > 64<<10 || limit < 1 || limit > 10000 || ttl < 0 || (ttl > 0 && ttl < time.Millisecond) {
		return errors.New("invalid bounded list entry")
	}
	return pushBoundedScript.Run(ctx, s.client, []string{s.listKey(namespace)}, value, limit, ttl.Milliseconds()).Err()
}

// ListBounded returns newest-first opaque values from a bounded list.
func (s *Store) ListBounded(ctx context.Context, namespace string, limit int) ([][]byte, error) {
	if s == nil {
		return nil, errors.New("redis store is not configured")
	}
	if namespace == "" || limit < 1 || limit > 10000 {
		return nil, errors.New("invalid bounded list query")
	}
	values, err := s.client.LRange(ctx, s.listKey(namespace), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	result := make([][]byte, len(values))
	for index, value := range values {
		result[index] = []byte(value)
	}
	return result, nil
}

// Redis Lua numbers cannot exactly represent counters above 2^53. Keep counter
// arithmetic in decimal strings and saturate unlimited dimensions at Go's MaxInt.
var fixedWindowScript = redis.NewScript(`
local function greater(a, b)
  if #a ~= #b then return #a > #b end
  return a > b
end
local function add(a, b)
  local result, carry = '', 0
  local i, j = #a, #b
  while i > 0 or j > 0 or carry > 0 do
    local x, y = 0, 0
    if i > 0 then x = tonumber(string.sub(a, i, i)) end
    if j > 0 then y = tonumber(string.sub(b, j, j)) end
    local sum = x + y + carry
    result = tostring(sum % 10) .. result
    carry = math.floor(sum / 10)
    i, j = i - 1, j - 1
  end
  return result
end
local count = #KEYS / 2
local requests, tokens, ttls = {}, {}, {}
for i = 1, count do
  local request_key, token_key = KEYS[i * 2 - 1], KEYS[i * 2]
  local current_requests = redis.call('GET', request_key) or '0'
  local current_tokens = redis.call('GET', token_key) or '0'
  requests[i] = add(current_requests, '1')
  tokens[i] = add(current_tokens, ARGV[1])
  local ttl = redis.call('PTTL', request_key)
  if ttl < 0 then ttl = redis.call('PTTL', token_key) end
  if ttl < 0 then ttl = tonumber(ARGV[2]) end
  ttls[i] = ttl
  local request_limit, token_limit = ARGV[4 + (i - 1) * 2], ARGV[5 + (i - 1) * 2]
  if (request_limit ~= '0' and greater(requests[i], request_limit)) or
     (token_limit ~= '0' and greater(tokens[i], token_limit)) then
    return {0, i - 1, ttl}
  end
end
for i = 1, count do
  if greater(requests[i], ARGV[3]) then requests[i] = ARGV[3] end
  if greater(tokens[i], ARGV[3]) then tokens[i] = ARGV[3] end
  redis.call('SET', KEYS[i * 2 - 1], requests[i], 'PX', math.max(ttls[i], 1))
  redis.call('SET', KEYS[i * 2], tokens[i], 'PX', math.max(ttls[i], 1))
end
return {1, -1, ttls[1]}
`)

func (s *Store) Allow(ctx context.Context, identity string, requestLimit, tokenLimit, tokens int, window time.Duration) (bool, time.Duration, error) {
	if s == nil {
		return false, 0, errors.New("redis store is not configured")
	}
	if requestLimit <= 0 && tokenLimit <= 0 {
		return true, 0, nil
	}
	allowed, _, retryAfter, err := s.AllowMany(ctx, []string{identity}, []int{requestLimit}, []int{tokenLimit}, tokens, window)
	return allowed, retryAfter, err
}

// AllowMany atomically reserves the same request against multiple fixed-window
// scopes. A rejection leaves every scope unchanged and returns its input index.
func (s *Store) AllowMany(ctx context.Context, identities []string, requestLimits, tokenLimits []int, tokens int, window time.Duration) (bool, int, time.Duration, error) {
	if s == nil {
		return false, -1, 0, errors.New("redis store is not configured")
	}
	if len(identities) == 0 || len(identities) != len(requestLimits) || len(identities) != len(tokenLimits) || len(identities) > 16 {
		return false, -1, 0, errors.New("invalid rate-limit scopes")
	}
	if tokens < 0 {
		tokens = 0
	}
	if window <= 0 {
		window = time.Minute
	}
	keys := make([]string, 0, len(identities)*2)
	arguments := make([]any, 0, 3+len(identities)*2)
	arguments = append(arguments, tokens, max(window.Milliseconds(), 1), math.MaxInt)
	for index, identity := range identities {
		if identity == "" || requestLimits[index] <= 0 && tokenLimits[index] <= 0 {
			return false, -1, 0, errors.New("invalid rate-limit scope")
		}
		identityHash := sha256.Sum256([]byte(identity))
		base := s.prefix + ":rate:" + hex.EncodeToString(identityHash[:16])
		keys = append(keys, base+":requests", base+":tokens")
		arguments = append(arguments, max(requestLimits[index], 0), max(tokenLimits[index], 0))
	}
	result, err := fixedWindowScript.Run(ctx, s.client, keys, arguments...).Slice()
	if err != nil {
		return false, -1, 0, err
	}
	if len(result) != 3 {
		return false, -1, 0, errors.New("unexpected redis rate-limit response")
	}
	allowed, ok := result[0].(int64)
	if !ok {
		return false, -1, 0, errors.New("invalid redis rate-limit decision")
	}
	rejected, ok := result[1].(int64)
	if !ok {
		return false, -1, 0, errors.New("invalid redis rate-limit scope")
	}
	ttlMS, ok := result[2].(int64)
	if !ok {
		return false, -1, 0, errors.New("invalid redis rate-limit ttl")
	}
	return allowed == 1, int(rejected), time.Duration(ttlMS) * time.Millisecond, nil
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

func (s *Store) listKey(namespace string) string {
	namespaceHash := sha256.Sum256([]byte(namespace))
	return s.prefix + ":list:" + hex.EncodeToString(namespaceHash[:16])
}

func (s *Store) circuitKeys(endpoint string) (state, probe, failures string) {
	endpointHash := sha256.Sum256([]byte(endpoint))
	base := s.prefix + ":circuit:" + hex.EncodeToString(endpointHash[:16])
	return base + ":open-until", base + ":probe", base + ":failures"
}
