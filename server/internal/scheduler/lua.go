package scheduler

// ─────────────────────────────────────────────
// Lua Scripts for Atomic Redis Operations
// ─────────────────────────────────────────────

// LuaFetchTask atomically claims a PENDING task for a worker node.
//
// KEYS[1] = task:{traceID}          (hash)
// ARGV[1] = nodeID
//
// Returns:
//
//	[1]  "OK"        – task successfully claimed
//	[1]  "GONE"      – task already claimed or doesn't exist
//	+ task fields    – gallery_id, gallery_key (when OK)
const LuaFetchTask = `
local taskKey  = KEYS[1]
local nodeID   = ARGV[1]

-- 1. Check task exists and is still PENDING
local status = redis.call("HGET", taskKey, "status")
if status ~= "PENDING" then
    return {"GONE"}
end

-- 2. Atomically set to PROCESSING and bind node
redis.call("HSET", taskKey,
    "status",  "PROCESSING",
    "node_id", nodeID
)

-- 3. Set crash-safety TTL (5 min) – if node dies, keys auto-expire.
redis.call("EXPIRE", taskKey, 300)

-- 4. Sync collapseKey TTL so it never outlives the task hash
local collapseKey = redis.call("HGET", taskKey, "collapse_key")
redis.call("EXPIRE", collapseKey, 300)

-- 5. Return task details needed by the node
local fields = redis.call("HMGET", taskKey, "gallery_id", "gallery_key")
return {"OK", fields[1], fields[2]}
`

// LuaCompleteTask stores the result in the per-user cache, then removes
// the task and its collapsing entries.
//
// KEYS[1] = task:{traceID}                (hash)
// ARGV[1] = archive URL
// ARGV[2] = cacheTTL (seconds)
// ARGV[3] = nodeID (requesting node)
//
// Returns: "OK", "INVALID", or "NODE_MISMATCH"
const LuaCompleteTask = `
local taskKey    = KEYS[1]
local archiveURL = ARGV[1]
local cacheTTL   = tonumber(ARGV[2])
local nodeID     = ARGV[3]

local status = redis.call("HGET", taskKey, "status")
if status ~= "PROCESSING" then
    return "INVALID"
end

local assignedNode = redis.call("HGET", taskKey, "node_id")
if assignedNode ~= nodeID then
    return "NODE_MISMATCH"
end

-- Read stored keys from task metadata
local cacheKey = redis.call("HGET", taskKey, "cache_key")

-- 1. Store result in per-user cache
redis.call("SET", cacheKey, archiveURL, "EX", cacheTTL)

-- 2. Remove task routing state and the task record itself
local collapseKey = redis.call("HGET", taskKey, "collapse_key")
redis.call("DEL", collapseKey)
redis.call("DEL", taskKey)

return "OK"
`

// LuaFailTask removes a task and cleans up collapsing entries.
//
// KEYS[1] = task:{traceID}                (hash)
// ARGV[1] = nodeID (optional; required when status=PROCESSING)
//
// Returns: "OK", "GONE", "NEED_NODE", or "NODE_MISMATCH"
const LuaFailTask = `
local taskKey = KEYS[1]
local nodeID  = ARGV[1]

local status = redis.call("HGET", taskKey, "status")
if not status then
    return "GONE"
end

if status == "PROCESSING" then
    if nodeID == "" then
        return "NEED_NODE"
    end

    local assignedNode = redis.call("HGET", taskKey, "node_id")
    if assignedNode ~= nodeID then
        return "NODE_MISMATCH"
    end
end

-- Remove task routing state and the task record itself.
local collapseKey = redis.call("HGET", taskKey, "collapse_key")
redis.call("DEL", collapseKey)
redis.call("DEL", taskKey)

return "OK"
`

// LuaPublishTask creates a new task hash if no inflight task exists
// for the same user+gallery (request collapsing).
//
// KEYS[1] = task:{traceID}                (hash to create)
// KEYS[2] = inflight:{userID}:{galleryID} (collapsing sentinel)
// KEYS[3] = cache:{userID}:{galleryID}    (per-user cached archive URL)
// ARGV[1] = traceID
// ARGV[2] = galleryID
// ARGV[3] = force   ("0" or "1")
// ARGV[4] = galleryKey
//
// Returns:
//
//	{"CREATED", traceID}    – new task created
//	{"COLLAPSED", traceID}  – existing inflight task reused
//	{"CACHED", archiveURL}  – result already cached (force=false only)
const LuaPublishTask = `
local taskKey      = KEYS[1]
local collapseKey  = KEYS[2]
local cacheKey     = KEYS[3]
local traceID      = ARGV[1]
local galleryID    = ARGV[2]
local force        = ARGV[3]
local galleryKey   = ARGV[4]

-- If force=false and cache already exists, return cached immediately.
if force == "0" then
    local cached = redis.call("GET", cacheKey)
    if cached then
        return {"CACHED", cached}
    end
end

-- Request Collapsing: if collapseKey exists, an identical task is already in-flight.
-- collapseKey TTL is always synced to be <= task hash TTL (enforced by LuaFetchTask),
-- so if collapseKey exists, the task is guaranteed to still be alive.
local existing = redis.call("GET", collapseKey)
if existing then
    return {"COLLAPSED", existing}
end

-- Create the task hash
redis.call("HSET", taskKey,
    "gallery_id",    galleryID,
    "gallery_key",   galleryKey,
    "collapse_key",  collapseKey,
    "cache_key",     cacheKey,
    "status",        "PENDING",
    "node_id",       ""
)
redis.call("EXPIRE", taskKey, 300)  -- crash-safety TTL (5 min)

-- Set collapsing sentinel (TTL synced with taskKey)
redis.call("SET", collapseKey, traceID, "EX", 300)

return {"CREATED", traceID}
`
