package infracachefeed

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

// 状态 Hash 同时保存事件收据和绝对分数；重复投递可修复未完成的 ZSET 写入
const applyHeatScript = `
local function has_type(key, expected)
  local value = redis.call('TYPE', key)
  local kind = type(value) == 'table' and value.ok or value
  return kind == 'none' or kind == expected
end
if not has_type(KEYS[1], 'hash') or not has_type(KEYS[2], 'zset') or not has_type(KEYS[3], 'hash') then
  return -4
end
local policy = redis.call('HGET', KEYS[3], 'policy')
if policy and policy ~= ARGV[1] then return -1 end
local state_policy = redis.call('HGET', KEYS[1], 'policy')
if state_policy and state_policy ~= ARGV[1] then return -1 end
if not state_policy and redis.call('EXISTS', KEYS[1]) == 1 then return -4 end
if not state_policy and redis.call('EXISTS', KEYS[2]) == 1 then return -4 end
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local start = tonumber(ARGV[7])
local hot_until = start + tonumber(ARGV[8]) + 60000
local expires_at = hot_until + tonumber(ARGV[9])
if tonumber(ARGV[5]) > now + 30000 or tonumber(ARGV[6]) > now + 30000 then return -5 end
if now >= expires_at or (now >= hot_until and redis.call('EXISTS', KEYS[2]) == 0) then return 0 end
local event_field = 'event:' .. ARGV[2]
local score_field = 'video:' .. ARGV[3]
local receipt = redis.call('HGET', KEYS[1], event_field)
if receipt and receipt ~= ARGV[4] then return -2 end
local previous = redis.call('HGET', KEYS[1], score_field)
if not previous and redis.call('ZSCORE', KEYS[2], ARGV[3]) then return -4 end
local score = 0
if previous then score = tonumber(previous) end
local videos = tonumber(redis.call('HGET', KEYS[1], 'videos') or '0')
local events = tonumber(redis.call('HGET', KEYS[1], 'events') or '0')
if not score or not videos or not events or videos < 0 or events < 0 or
   score ~= math.floor(score) or videos ~= math.floor(videos) or events ~= math.floor(events) or
   math.abs(score) > 1000000000 or videos > tonumber(ARGV[12]) or events > tonumber(ARGV[13]) then return -4 end
if receipt and not previous then return -4 end
if not receipt then
  if events >= tonumber(ARGV[13]) or (not previous and videos >= tonumber(ARGV[12])) then return -3 end
  score = score + tonumber(ARGV[11])
  if math.abs(score) > 1000000000 then return -4 end
  if not previous then videos = videos + 1 end
  events = events + 1
end
if not policy then
  redis.call('HSET', KEYS[3], 'policy', ARGV[1], 'coverage', 'unverified')
end
if not receipt then
  redis.call('HSET', KEYS[1], event_field, ARGV[4], score_field, string.format('%.0f', score),
    'videos', videos, 'events', events, 'policy', ARGV[1])
end
local state_ttl = math.max(tonumber(ARGV[10]), expires_at - now)
local old_ttl = redis.call('PTTL', KEYS[1])
redis.call('PEXPIRE', KEYS[1], math.max(state_ttl, old_ttl))
redis.call('ZADD', KEYS[2], score, ARGV[3])
redis.call('PEXPIREAT', KEYS[2], expires_at)
if receipt then return 2 end
return 1`

type HeatIndexOptions struct {
	Generation       string
	KeyPrefix        string
	OperationTimeout time.Duration
	Policy           domainfeed.HeatPolicy
}

type HeatIndex struct {
	client  ScriptCache
	options HeatIndexOptions
}

var _ domainfeed.HeatIndex = (*HeatIndex)(nil)

func NewHeatIndex(client ScriptCache, options HeatIndexOptions) (*HeatIndex, error) {
	if client == nil {
		return nil, domainfeed.ErrHeatUnavailable
	}
	if err := options.Policy.Validate(); err != nil {
		return nil, err
	}
	if len(options.Generation) < 1 || len(options.Generation) > 64 {
		return nil, domainfeed.ErrInvalidHeatPolicy
	}
	for _, value := range options.Generation {
		if (value < 'a' || value > 'z') && (value < 'A' || value > 'Z') &&
			(value < '0' || value > '9') && value != '-' && value != '_' {
			return nil, domainfeed.ErrInvalidHeatPolicy
		}
	}
	if options.KeyPrefix == "" {
		options.KeyPrefix = "gofeed:feed:heat:v1"
	}
	if len(options.KeyPrefix) > 200 || strings.ContainsAny(options.KeyPrefix, "{}\r\n\t ") {
		return nil, domainfeed.ErrInvalidHeatPolicy
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = 100 * time.Millisecond
	}
	if options.OperationTimeout < 0 || options.OperationTimeout > time.Second {
		return nil, domainfeed.ErrInvalidHeatPolicy
	}
	return &HeatIndex{client: client, options: options}, nil
}

// ApplyHeat 原子保存去重收据和分数，撤销回到原创建分钟且不截断负值
func (h *HeatIndex) ApplyHeat(ctx context.Context, mutation domainfeed.HeatMutation) (domainfeed.HeatResult, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := mutation.Validate(); err != nil {
		return "", err
	}
	score, err := h.options.Policy.Score(mutation.Kind, mutation.Delta)
	if err != nil {
		return "", err
	}
	start := mutation.InteractionCreatedAt.UTC().Truncate(time.Minute)
	base := h.options.KeyPrefix + ":{" + h.options.Generation + "}:"
	minute := strconv.FormatInt(start.Unix(), 10)
	keys := []string{base + "state:" + minute, base + "minute:" + minute, base + "meta"}
	policy := h.options.Policy
	opCtx, cancel := context.WithTimeout(ctx, h.options.OperationTimeout)
	defer cancel()
	raw, err := h.client.Eval(opCtx, applyHeatScript, keys,
		policy.Fingerprint(), strings.ToLower(mutation.EventID), strconv.FormatUint(uint64(mutation.VideoID), 10),
		mutation.Fingerprint(), mutation.OccurredAt.UnixMilli(), mutation.InteractionCreatedAt.UnixMilli(),
		start.UnixMilli(), policy.Window.Milliseconds(), policy.RetentionGrace.Milliseconds(),
		policy.DedupeTTL.Milliseconds(), score, policy.MaxVideosPerMinute, policy.MaxEventsPerMinute)
	if err != nil {
		return "", fmt.Errorf("%w: %w", domainfeed.ErrHeatUnavailable, err)
	}
	value, ok := raw.(int64)
	if !ok {
		return "", domainfeed.ErrHeatUnavailable
	}
	switch value {
	case 0:
		return domainfeed.HeatSkippedExpired, nil
	case 1:
		return domainfeed.HeatApplied, nil
	case 2:
		return domainfeed.HeatDuplicate, nil
	case -1:
		return "", domainfeed.ErrHeatPolicyConflict
	case -2:
		return "", domainfeed.ErrHeatEventConflict
	case -3:
		return "", domainfeed.ErrHeatCapacity
	case -5:
		return "", domainfeed.ErrHeatFutureEvent
	default:
		return "", domainfeed.ErrHeatUnavailable
	}
}
