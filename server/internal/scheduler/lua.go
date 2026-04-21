package scheduler

// ─────────────────────────────────────────────
// Lua Scripts for Atomic Redis Operations
// ─────────────────────────────────────────────

// LuaFetchTask atomically claims a PENDING task for a worker node.
//
// KEYS[1] = task:{traceID}          (hash)
// ARGV[1] = nodeID
// ARGV[2] = leaseTTL (seconds)
//
// Returns:
//
//	[1]  "OK"        – task successfully claimed
//	[1]  "GONE"      – task already claimed or doesn't exist
//	+ task fields    – gallery_id, gallery_key (when OK)
const LuaFetchTask = `
local taskKey  = KEYS[1]
local nodeID   = ARGV[1]
local leaseTTL = tonumber(ARGV[2])

-- 1. Check task exists and is still PENDING
local status = redis.call("HGET", taskKey, "status")
if status ~= "PENDING" then
    return {"GONE"}
end

-- 2. Atomically set to PROCESSING and bind node
redis.call("HMSET", taskKey,
    "status",  "PROCESSING",
    "node_id", nodeID
)

-- 3. Set lease TTL – if not reported back within this window, key expires
--    and lease watchdog can re-enqueue.
redis.call("EXPIRE", taskKey, leaseTTL)

-- 4. Sync collapseKey TTL so it never outlives the task hash
local collapseKey = redis.call("HGET", taskKey, "collapse_key")
redis.call("EXPIRE", collapseKey, leaseTTL)

-- 5. Return task details needed by the node
local fields = redis.call("HMGET", taskKey, "gallery_id", "gallery_key")
return {"OK", fields[1], fields[2]}
`

// LuaCompleteTask atomically marks a task as COMPLETED, stores the result
// in the per-user cache, and cleans up collapsing/queue entries.
//
// KEYS[1] = task:{traceID}                (hash)
// ARGV[1] = archive URL
// ARGV[2] = cacheTTL (seconds)
// ARGV[3] = nodeID (requesting node)
// ARGV[4] = traceID
//
// Returns: "OK", "INVALID", or "NODE_MISMATCH"
const LuaCompleteTask = `
local taskKey    = KEYS[1]
local archiveURL = ARGV[1]
local cacheTTL   = tonumber(ARGV[2])
local nodeID     = ARGV[3]
local traceID    = ARGV[4]

local status = redis.call("HGET", taskKey, "status")
if status ~= "PROCESSING" then
    return "INVALID"
end

local assignedNode = redis.call("HGET", taskKey, "node_id")
if assignedNode ~= nodeID then
    return "NODE_MISMATCH"
end

-- Read stored keys from task metadata
local keys = redis.call("HMGET", taskKey, "cache_key", "collapse_key")
local cacheKey    = keys[1]
local collapseKey = keys[2]

-- 1. Mark task done
redis.call("HSET", taskKey, "status", "COMPLETED")
redis.call("EXPIRE", taskKey, 300)

-- 2. Store result in per-user cache
redis.call("SET", cacheKey, archiveURL, "EX", cacheTTL)

-- 3. Remove collapsing key
redis.call("DEL", collapseKey)

-- 4. Remove from pending queue
redis.call("LREM", "queue:pending", 0, traceID)

return "OK"
`

// LuaFailTask finalizes a task and cleans up collapsing/queue entries.
//
// KEYS[1] = task:{traceID}                (hash)
// ARGV[1] = nodeID (optional; required when status=PROCESSING)
// ARGV[2] = traceID
// ARGV[3] = mode   ("FAIL" or "REJECT")
//
// Returns: "OK", "GONE", "INVALID", "NEED_NODE", or "NODE_MISMATCH"
const LuaFailTask = `
local taskKey = KEYS[1]
local nodeID  = ARGV[1]
local traceID = ARGV[2]
local mode    = ARGV[3]

if mode ~= "REJECT" then
    mode = "FAIL"
end

local status = redis.call("HGET", taskKey, "status")
if not status then
    return "GONE"
end

if status == "COMPLETED" or status == "FAILED" then
    return "INVALID"
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

-- Read stored collapse key from task metadata
local collapseKey = redis.call("HGET", taskKey, "collapse_key")

-- 1. Remove collapsing key so future requests do not collapse into this trace
redis.call("DEL", collapseKey)

-- 2. Remove from pending queue
redis.call("LREM", "queue:pending", 0, traceID)

-- 3. Finalize according to mode
if mode == "REJECT" then
    redis.call("DEL", taskKey)
else
    redis.call("HSET", taskKey, "status", "FAILED")
    redis.call("EXPIRE", taskKey, 300)
end

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
// ARGV[4] = force   ("0" or "1")
// ARGV[5] = leaseTTL (seconds) – used for inflight key expiry
// ARGV[6] = galleryKey
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
local leaseTTL     = tonumber(ARGV[4])
local galleryKey   = ARGV[5]

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
redis.call("HMSET", taskKey,
    "gallery_id",    galleryID,
    "gallery_key",   galleryKey,
    "collapse_key",  collapseKey,
    "cache_key",     cacheKey,
    "status",        "PENDING",
    "force",         force,
    "free_tier",     "0",
    "estimated_gp",  "0",
    "node_id",       ""
)
redis.call("EXPIRE", taskKey, leaseTTL * 3)  -- generous TTL for the hash itself

-- Set collapsing sentinel
redis.call("SET", collapseKey, traceID, "EX", leaseTTL * 2)

-- Push to pending queue
redis.call("RPUSH", "queue:pending", traceID)

return {"CREATED", traceID}
`

// LuaReclaimTask handles timeout/failure recovery:
// - Resets PROCESSING tasks back to PENDING
// - Re-sets the collapsing key to protect from duplication
// - Re-enqueues the task
//
// KEYS[1] = task:{traceID}                (hash)
// ARGV[1] = leaseTTL (seconds)
// ARGV[2] = traceID
//
// Returns:
//
//	"RECLAIMED"    – task was reset and re-enqueued
//	"NOT_NEEDED"   – task is not in PROCESSING state or doesn't exist
const LuaReclaimTask = `
local taskKey  = KEYS[1]
local leaseTTL = tonumber(ARGV[1])
local traceID  = ARGV[2]

-- Check if task exists and is PROCESSING
local status = redis.call("HGET", taskKey, "status")
if status ~= "PROCESSING" then
    return "NOT_NEEDED"
end

-- Reset to PENDING and clear node assignment
redis.call("HMSET", taskKey,
    "status",  "PENDING",
    "node_id", ""
)
redis.call("EXPIRE", taskKey, leaseTTL * 3)

-- Re-enqueue
redis.call("RPUSH", "queue:pending", traceID)

-- Re-set collapsing key to protect the re-enqueued task from duplication
local collapseKey = redis.call("HGET", taskKey, "collapse_key")
redis.call("SET", collapseKey, traceID, "EX", leaseTTL * 2)

return "RECLAIMED"
`
