package tokenbucket

// ─────────────────────────────────────────────
// Lua Scripts for Atomic Redis Token Bucket Operations
// ─────────────────────────────────────────────

// LuaConsumeTokens atomically refills and deducts tokens from a user's bucket.
//
// KEYS[1] = tokens:{userID}
// ARGV[1] = amount (tokens to consume, positive integer)
// ARGV[2] = rate (tokens per second)
// ARGV[3] = max_capacity
// ARGV[4] = now_sec (current unix timestamp in seconds)
// ARGV[5] = ttl_sec (key expiry in seconds, = max_capacity / rate)
//
// Returns:
//
//	"OK"             – tokens consumed
//	"INSUFFICIENT"   – not enough tokens
//
// When the key does not exist the bucket is initialised at max_capacity.
// When tokens are insufficient we still update updated_at so the user
// does not gain extra refill on the next attempt.
const LuaConsumeTokens = `
local key         = KEYS[1]
local amount      = tonumber(ARGV[1])
local rate        = tonumber(ARGV[2])
local maxCapacity = tonumber(ARGV[3])
local nowSec      = tonumber(ARGV[4])
local ttlSec      = tonumber(ARGV[5])

-- Read current state (or initialise full bucket)
local fields = redis.call("HMGET", key, "tokens", "updated_at")
local tokens    = tonumber(fields[1])
local updatedAt = tonumber(fields[2])

if tokens == nil or updatedAt == nil then
    tokens    = maxCapacity
    updatedAt = nowSec
end

-- Refill: add tokens for elapsed time since last update
local elapsed = nowSec - updatedAt
tokens = tokens + elapsed * rate
if tokens > maxCapacity then tokens = maxCapacity end

-- Check sufficiency
if tokens >= amount then
    tokens = tokens - amount
    redis.call("HSET", key, "tokens", tokens, "updated_at", nowSec)
    redis.call("EXPIRE", key, ttlSec)
    return "OK"
end

-- Insufficient: still update timestamp to prevent refill-on-retry
redis.call("HSET", key, "tokens", tokens, "updated_at", nowSec)
redis.call("EXPIRE", key, ttlSec)
return "INSUFFICIENT"
`

// LuaRefundTokens atomically refills and adds tokens back to a user's bucket.
//
// KEYS[1] = tokens:{userID}
// ARGV[1] = amount (tokens to refund, positive integer)
// ARGV[2] = rate (tokens per second)
// ARGV[3] = max_capacity
// ARGV[4] = now_sec (current unix timestamp in seconds)
// ARGV[5] = ttl_sec (key expiry in seconds, = max_capacity / rate)
//
// Returns "OK" on success.
const LuaRefundTokens = `
local key         = KEYS[1]
local amount      = tonumber(ARGV[1])
local rate        = tonumber(ARGV[2])
local maxCapacity = tonumber(ARGV[3])
local nowSec      = tonumber(ARGV[4])
local ttlSec      = tonumber(ARGV[5])

-- Read current state (or initialise full bucket)
local fields = redis.call("HMGET", key, "tokens", "updated_at")
local tokens    = tonumber(fields[1])
local updatedAt = tonumber(fields[2])

if tokens == nil or updatedAt == nil then
    tokens    = maxCapacity
    updatedAt = nowSec
end

-- Refill, then add refund, capped at maxCapacity
local elapsed = nowSec - updatedAt
tokens = tokens + elapsed * rate
if tokens > maxCapacity then tokens = maxCapacity end
tokens = tokens + amount
if tokens > maxCapacity then tokens = maxCapacity end

redis.call("HSET", key, "tokens", tokens, "updated_at", nowSec)
redis.call("EXPIRE", key, ttlSec)
return "OK"
`
