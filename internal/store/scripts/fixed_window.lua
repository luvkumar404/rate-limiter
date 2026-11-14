-- Unix-epoch-aligned fixed counter. ARGV: limit, window_us, unused ttl, unused ID.
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000000 + tonumber(clock[2])
local state = redis.call('HMGET', KEYS[1], 'window_start', 'count', 'last_seen')
local start = nil
local count = 0
if state[1] or state[2] or state[3] then
    start = tonumber(state[1])
    count = tonumber(state[2])
    local last = tonumber(state[3])
    if not start or not count or not last or start < 0 or count < 0 or last < 0
        or start ~= start or count ~= count or last ~= last
        or start == math.huge or count == math.huge or last == math.huge
        or count ~= math.floor(count) then
        return redis.error_reply('invalid fixed window state')
    end
    now = math.max(now,last)
end
local current = now - (now % window)
if start ~= current then start = current; count = 0 end
local allowed = 0
local retry = 0
if count < limit then count = count + 1; allowed = 1
else retry = math.max(1, math.ceil(start + window - now)) end
redis.call('HSET', KEYS[1], 'window_start', string.format('%.0f',start),
    'count', string.format('%.0f',count), 'last_seen', string.format('%.0f',now))
redis.call('PEXPIRE', KEYS[1], math.ceil((start + window - now)/1000)+1)
return {allowed, math.max(0,limit-count), retry}
