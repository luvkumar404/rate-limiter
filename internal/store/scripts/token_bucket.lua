-- One atomic decision. Time comes exclusively from Redis, in microseconds.
-- KEYS[1]: bucket hash; ARGV: refill limit, capacity, window_us, ttl_ms.
local limit = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local window = tonumber(ARGV[3])
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000000 + tonumber(clock[2])
local state = redis.call('HMGET', KEYS[1], 'tokens', 'last_refill')
local tokens = capacity
local last = now
if state[1] or state[2] then
    tokens = tonumber(state[1])
    last = tonumber(state[2])
    if not tokens or not last or tokens < 0 or last < 0
        or tokens ~= tokens or last ~= last
        or tokens == math.huge or last == math.huge then
        return redis.error_reply('invalid token bucket state')
    end
end
-- Guard against Redis wall-clock regression without granting duplicate refill.
now = math.max(now, last)
tokens = math.min(capacity, tokens + (now - last) * limit / window)
local allowed = 0
local retry = 0
if tokens >= 1 then
    tokens = tokens - 1
    allowed = 1
else
    retry = math.ceil((1 - tokens) * window / limit)
end
redis.call('HSET', KEYS[1], 'tokens', string.format('%.17g', tokens),
    'last_refill', string.format('%.0f', now))
redis.call('PEXPIRE', KEYS[1], ARGV[4])
return {allowed, math.floor(tokens), retry}
