-- Exact log of admitted events in (now - window, now]. Denials add no events.
-- KEYS: event sorted set, monotonic clock string. ARGV: limit, window_us, ttl_ms, event_id.
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000000 + tonumber(clock[2])
local prior = redis.call('GET', KEYS[2])
if prior then
    local last = tonumber(prior)
    if not last or last < 0 or last ~= last or last == math.huge then
        return redis.error_reply('invalid sliding window clock')
    end
    now = math.max(now, last)
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', string.format('%.0f', now-window))
local count = redis.call('ZCARD', KEYS[1])
local allowed = 0
local retry = 0
if count < limit then
    local added = redis.call('ZADD', KEYS[1], 'NX', string.format('%.0f', now), ARGV[4])
    if added ~= 1 then return redis.error_reply('duplicate sliding window event ID') end
    allowed = 1
    count = count + 1
else
    local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
    retry = math.max(1, math.ceil(tonumber(oldest[2]) + window - now))
end
redis.call('SET', KEYS[2], string.format('%.0f', now), 'PX', ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return {allowed, math.max(0, limit-count), retry}
