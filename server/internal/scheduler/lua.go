package scheduler

// ─────────────────────────────────────────────
// Lua Scripts for Atomic Redis Operations
// ─────────────────────────────────────────────

// LuaPublishTask atomically checks cache / collapsing state and creates a
// collapsing sentinel when neither exists.
//
// KEYS[1] = collapseKey  inflight:{userID}:{galleryID}
// KEYS[2] = cacheKey     cache:{userID}:{galleryID}
// ARGV[1] = traceID
// ARGV[2] = force        ("0" or "1")
//
// Returns:
//
//	{"CREATED", traceID}    – new sentinel created
//	{"COLLAPSED", traceID}  – existing inflight task reused
//	{"CACHED", archiveURL}  – result already cached (force=false only)
const LuaPublishTask = `
local collapseKey = KEYS[1]
local cacheKey    = KEYS[2]
local traceID     = ARGV[1]
local force       = ARGV[2]

-- If force=false and cache already exists, return cached immediately.
if force == "0" then
    local cached = redis.call("GET", cacheKey)
    if cached then
        return {"CACHED", cached}
    end
end

-- Request Collapsing: if collapseKey exists, an identical task is already in-flight.
local existing = redis.call("GET", collapseKey)
if existing then
    return {"COLLAPSED", existing}
end

-- Create collapsing sentinel
redis.call("SET", collapseKey, traceID, "EX", 300)

return {"CREATED", traceID}
`
