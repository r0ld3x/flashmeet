# FlashMeet

Random video chat. Connect with strangers through video, audio, and text -- built with Go, WebRTC, and Redis.

**[flashmeet.tech](https://flashmeet.tech)**

[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white)](https://golang.org/)
[![Redis](https://img.shields.io/badge/Redis-7+-DC382D?logo=redis&logoColor=white)](https://redis.io/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

---

## How it works

1. User opens the app, clicks **Connect** -- a WebSocket connection is established with an HMAC-signed session token
2. User clicks **Find Match** -- they enter a Redis Stream-backed matchmaking queue
3. The matchmaker consumer pulls users from the stream in batches, checks liveness, and pairs them using an atomic Lua script
4. Both users receive `match_found` with assigned roles (caller/callee)
5. The caller creates a WebRTC offer and sends it through the signaling server
6. ICE candidates and SDP are exchanged via WebSocket, then a direct P2P video/audio connection is established
7. Users can chat via text, skip to the next person, or disconnect at any time

When one user disconnects, their partner is automatically re-queued for a new match.

---

## Architecture

```
                    P2P Video/Audio (WebRTC)
            ┌─────────────────────────────────────┐
            │                                     │
       ┌────┴─────┐                         ┌─────┴────┐
       │ Client A │                         │ Client B │
       └────┬─────┘                         └─────┬────┘
            │ WebSocket                  WebSocket │
            │                                     │
     ┌──────┴─────────────────────────────────────┴──────┐
     │               Load Balancer (optional)            │
     └──────┬─────────────────────────────────────┬──────┘
            │                                     │
     ┌──────┴──────┐                       ┌──────┴──────┐
     │  Server 1   │                       │  Server 2   │
     │  (Go/Echo)  │                       │  (Go/Echo)  │
     │             │                       │             │
     │ - WebSocket │                       │ - WebSocket │
     │ - Signaling │                       │ - Signaling │
     │ - Matchmaker│                       │ - Matchmaker│
     └──────┬──────┘                       └──────┬──────┘
            │                                     │
            └──────────────┬──────────────────────┘
                           │
                    ┌──────┴──────┐
                    │    Redis    │
                    │             │
                    │ Streams     │  ← matchmaking queue
                    │ Pub/Sub     │  ← cross-server signaling
                    │ Keys        │  ← client/match/server state
                    │ Lua Scripts │  ← atomic match creation
                    └─────────────┘
```

### Redis key layout

| Key pattern          | Type                    | Purpose                               | TTL           | Refresh                 |
| -------------------- | ----------------------- | ------------------------------------- | ------------- | ----------------------- |
| `client:{id}`        | String → serverID       | Tracks which server a client is on    | 60s           | Every 45s via keepAlive |
| `user_match:{id}`    | String → matchID        | Prevents double-matching              | 5 min         | Every 45s via keepAlive |
| `match:{id}`         | Hash                    | Match state (u1, u2, connected flags) | 5 min         | Every 45s via keepAlive |
| `server:{id}`        | String                  | Server heartbeat                      | 60s           | Every 55s via goroutine |
| `signal:{serverID}`  | Pub/Sub channel         | Routes messages to the right server   | --            | --                      |
| `matchmaking_stream` | Stream + consumer group | Matchmaking queue                     | capped at 10k | --                      |
| `ratelimit:{ip}`     | Counter                 | Per-IP rate limiting                  | 1 min         | Never (sliding window)  |
| `ban:{ip}`           | String                  | IP ban with reason                    | configurable  | Never (admin-set)       |

TTLs act as a crash recovery safety net, not session timers. Each key is refreshed periodically while the connection is alive. If a server crashes, keys expire naturally and orphaned state cleans up within 5 minutes.

### Matchmaking pipeline

The matchmaker runs as a goroutine on each server, consuming from a shared Redis Stream consumer group (`matchmakers`). Multiple servers share the workload automatically.

Each processing cycle:

| Phase     | Operation                                                                                               | Redis round-trips |
| --------- | ------------------------------------------------------------------------------------------------------- | ----------------- |
| 1         | Pipeline `GET` all `client:{id}` + `user_match:{id}` keys                                               | 1                 |
| 2         | Filter dead/already-matched users, pipeline `XACK` their messages                                       | 1                 |
| 3         | Pipeline `EVALSHA` (Lua script) for each valid pair -- atomically locks both users + creates match hash | 1                 |
| 4         | Pipeline `XACK` paired messages + `PUBLISH` notifications                                               | 2                 |
| **Total** | **100 users matched in ~5 round-trips**                                                                 | **~5**            |

The Lua script ensures match creation is atomic -- if two matchmaker instances try to match the same user simultaneously, only one succeeds:

```lua
SET user_match:{u1} matchID NX EX 300    -- lock u1
SET user_match:{u2} matchID NX EX 300    -- lock u2 (rolls back u1 on failure)
HSET match:{id} u1 u2 connected flags   -- create match state
```

---

## WebSocket protocol

All messages use `{ "op": "...", "data": {...} }` format.

### Client to server

| op              | data                     | description                    |
| --------------- | ------------------------ | ------------------------------ |
| `join_queue`    | --                       | Enter matchmaking queue        |
| `next`          | --                       | Leave current match, find next |
| `disconnect`    | --                       | Disconnect from match          |
| `chat`          | `{ "message": "..." }`   | Send text to matched partner   |
| `webrtc_offer`  | `{ "sdp": "..." }`       | Send SDP offer                 |
| `webrtc_answer` | `{ "sdp": "..." }`       | Send SDP answer                |
| `ice_candidate` | `{ "candidate": {...} }` | Send ICE candidate             |

### Server to client

| op                     | data                                                | description                |
| ---------------------- | --------------------------------------------------- | -------------------------- |
| `match_found`          | `{ "match_id": "...", "role": "caller\|callee" }`   | Matched with a partner     |
| `partner_disconnected` | --                                                  | Partner left               |
| `chat`                 | `{ "message": "..." }`                              | Text from partner          |
| `webrtc_offer`         | `{ "data": "sdp..." }`                              | SDP offer from partner     |
| `webrtc_answer`        | `{ "data": { "sdp": "...", "from": "..." } }`       | SDP answer from partner    |
| `ice_candidate`        | `{ "data": { "candidate": {...}, "from": "..." } }` | ICE candidate from partner |

---

## Project structure

```
flashmeet/
├── main.go                 Entry point, routes, server init
├── client.go               Client struct, writePump, keepAlive
├── handle_websocket.go     WebSocket upgrade, connection lifecycle
├── incoming.go             readPump, message router, disconnect/next handlers
├── handle_webrtc.go        WebRTC signaling (offer/answer/ICE forwarding)
├── join_queue.go           Queue entry point
├── chat.go                 Chat message forwarding
├── redis/
│   ├── client.go           Redis connection init
│   ├── operations.go       Matchmaker, Lua scripts, signaling, pub/sub
│   ├── chat.go             Chat history storage
│   └── ips.go              IP ban management
├── middleware/
│   ├── session_token.go    HMAC token generation/verification
│   └── is_allowed.go       Rate limiting, origin checks
├── helper/
│   └── helper.go           Real IP extraction (CF, XFF)
├── Dockerfile              Multi-stage distroless build
├── docker-compose.yml      App + Redis
└── Makefile                Build/run/deploy shortcuts
```

---

## Running locally

### Prerequisites

- Go 1.23+
- Redis 7+

### Start

```bash
# start redis
redis-server

# run the app
go run .

# open http://localhost:8080
```

### Docker

```bash
# start everything
docker compose up -d

# or pull the pre-built image
docker compose pull && docker compose up -d
```

### Environment variables

| Variable     | Default     | Description    |
| ------------ | ----------- | -------------- |
| `REDIS_HOST` | `localhost` | Redis hostname |
| `REDIS_PORT` | `6379`      | Redis port     |
| `REDIS_PASS` | --          | Redis password |

---

## Horizontal scaling

Each server instance is stateless -- all coordination happens through Redis.

```
                  Load Balancer
                 (sticky sessions)
              ┌───────┼───────┐
              ▼       ▼       ▼
          Server 1  Server 2  Server 3
              │       │       │
              └───────┼───────┘
                      ▼
                    Redis
```

What makes this work:

- **Client routing**: `client:{id}` maps each user to their server. Any server can send a message to any user via `PUBLISH signal:{targetServerID}`.
- **Matchmaking**: Redis Streams consumer group distributes queue entries across all matchmaker instances. Each server processes its share.
- **Atomic matching**: Lua scripts prevent race conditions when multiple matchmakers try to match the same user.
- **Server registry**: Each server heartbeats `server:{id}` every 55 seconds. Stale servers' unprocessed stream messages are reclaimed via `XAUTOCLAIM`.

### Scaling limits

| Component               | Bottleneck                        | Mitigation                                         |
| ----------------------- | --------------------------------- | -------------------------------------------------- |
| Redis (single instance) | All state flows through one Redis | Separate instances by concern, or Redis Cluster    |
| Pub/Sub fanout          | Broadcast to all cluster nodes    | Use Redis Streams for signaling at very high scale |
| WebSocket connections   | RAM per server (~100KB/conn)      | Add more servers behind LB                         |

---

## Security

- **Session tokens**: HMAC-SHA256 signed, time-limited
- **Rate limiting**: Per-IP connection throttling via Redis counters
- **IP banning**: Redis-backed with configurable TTL
- **Origin validation**: WebSocket upgrade checks allowed origins
- **Real IP detection**: Supports `CF-Connecting-IP`, `X-Forwarded-For`

---

## License

[MIT](LICENSE)
