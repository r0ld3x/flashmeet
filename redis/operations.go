package redis

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var clientKey = "client:%s" // client:{clientID} -> {serverID}

/********************************
 * CLIENT META
 ********************************/

func RegisterClient(clientID, ip, serverID string) error {
	key := fmt.Sprintf(clientKey, clientID)
	return Client.Set(Ctx, key, serverID, 60*time.Second).Err()
}

func GetClient(clientID string) (string, error) {
	key := fmt.Sprintf(clientKey, clientID)
	serverId, err := Client.Get(Ctx, key).Result()
	if err != nil {
		return "", err
	}
	return serverId, nil
}

func RefreshClient(clientID, serverID string) {
	key := fmt.Sprintf(clientKey, clientID)
	Client.Set(Ctx, key, serverID, 60*time.Second)
}

/********************************
 * SERVER REGISTRATION
 ********************************/

func RegisterServer(serverID string) {
	key := "server:" + serverID

	err := Client.Set(Ctx, key, time.Now().Unix(), 60*time.Second).Err()
	if err != nil {
		log.Fatal("failed to register server:", err)
	}

	go func() {
		ticker := time.NewTicker(55 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			Client.Set(Ctx, key, time.Now().Unix(), 60*time.Second)
		}
	}()

	log.Println("Server registered:", serverID)
}

/********************************
 * SEND TO CLIENT (PUBSUB ROUTING)
 ********************************/

func SendToClient(clientID string, payload any) error {
	meta, err := GetClient(clientID)
	if err != nil {
		return err
	}
	channel := "signal:" + meta

	msg := map[string]any{
		"client_id": clientID,
		"payload":   payload,
	}

	log.Printf("Publishing to channel %s: %v\n", channel, msg)
	b, _ := json.Marshal(msg)

	return Client.Publish(Ctx, channel, b).Err()
}

/********************************
 * SIGNAL SUBSCRIBER (WS SERVER)
 ********************************/

func StartSignalSubscriber(serverID string, handler func(userID string, payload json.RawMessage)) {
	ch := Client.Subscribe(Ctx, "signal:"+serverID).Channel()

	go func() {
		for msg := range ch {
			var incoming struct {
				UserID  string          `json:"client_id"`
				Payload json.RawMessage `json:"payload"`
			}

			if err := json.Unmarshal([]byte(msg.Payload), &incoming); err != nil {
				log.Println("signal parse error:", err)
				continue
			}

			handler(incoming.UserID, incoming.Payload)
		}
	}()
}

/********************************
 * MATCHMAKER
 ********************************/

const (
	streamName = "matchmaking_stream"
	groupName  = "matchmakers"
)

type queuedUser struct {
	userID string
	msgID  string
}

var matchScript = redis.NewScript(`
local u1_lock = redis.call('SET', KEYS[1], ARGV[1], 'NX', 'EX', 300)
if not u1_lock then return 0 end
local u2_lock = redis.call('SET', KEYS[2], ARGV[1], 'NX', 'EX', 300)
if not u2_lock then
    redis.call('DEL', KEYS[1])
    return 0
end
redis.call('HSET', KEYS[3],
    'u1', ARGV[2], 'u2', ARGV[3],
    'u1_connected', 1, 'u2_connected', 1,
    'created_at', ARGV[4])
redis.call('EXPIRE', KEYS[3], 300)
return 1
`)

func LoadMatchScript() {
	if err := matchScript.Load(Ctx, Client).Err(); err != nil {
		log.Fatal("failed to load match script:", err)
	}
	log.Println("Match Lua script loaded")
}

func StartMatchmaker(serverId string) {
	buffer := []queuedUser{}
	autoClaimStart := "0-0"

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			messages, next, err := Client.XAutoClaim(Ctx, &redis.XAutoClaimArgs{
				Stream:   streamName,
				Group:    groupName,
				Consumer: serverId,
				MinIdle:  60000,
				Start:    autoClaimStart,
				Count:    20,
			}).Result()

			if err == nil {
				autoClaimStart = next
				for _, msg := range messages {
					func(m redis.XMessage) {
						defer func() {
							if r := recover(); r != nil {
								log.Printf("PANIC while processing message %s: %v", m.ID, r)
								if err := Client.XAck(Ctx, streamName, groupName, m.ID).Err(); err != nil {
									log.Printf("Failed to ACK after panic: %v", err)
								}
							}
						}()

						buffer = append(buffer, queuedUser{
							userID: m.Values["client_id"].(string),
							msgID:  m.ID,
						})
					}(msg)
				}
			}

		default:
			res, err := Client.XReadGroup(Ctx, &redis.XReadGroupArgs{
				Group:    groupName,
				Consumer: serverId,
				Streams:  []string{streamName, ">"},
				Count:    100,
				Block:    500 * time.Millisecond,
			}).Result()

			if err == nil {
				for _, stream := range res {
					for _, msg := range stream.Messages {
						buffer = append(buffer, queuedUser{
							userID: msg.Values["client_id"].(string),
							msgID:  msg.ID,
						})
					}
				}
			}
		}

		buffer = processBuffer(buffer)
	}
}

func processBuffer(buffer []queuedUser) []queuedUser {
	if len(buffer) < 2 {
		return buffer
	}

	// Phase 1: bulk presence check — 1 round-trip
	pipe := Client.Pipeline()
	clientCmds := make([]*redis.StringCmd, len(buffer))
	matchCmds := make([]*redis.StringCmd, len(buffer))

	for i, u := range buffer {
		clientCmds[i] = pipe.Get(Ctx, fmt.Sprintf(clientKey, u.userID))
		matchCmds[i] = pipe.Get(Ctx, "user_match:"+u.userID)
	}
	pipe.Exec(Ctx)

	// Build userID -> serverID map for notifications later
	servers := make(map[string]string, len(buffer))
	for i, u := range buffer {
		if srv, err := clientCmds[i].Result(); err == nil {
			servers[u.userID] = srv
		}
	}

	// Phase 2: filter valid users, bulk-ACK dead/already-matched — 1 round-trip
	var valid []queuedUser
	ackPipe := Client.Pipeline()

	for i, u := range buffer {
		_, clientErr := clientCmds[i].Result()
		_, matchErr := matchCmds[i].Result()

		isAlive := clientErr == nil
		isMatched := matchErr == nil

		if !isAlive || isMatched {
			ackPipe.XAck(Ctx, streamName, groupName, u.msgID)
		} else {
			valid = append(valid, u)
		}
	}
	ackPipe.Exec(Ctx)

	if len(valid) < 2 {
		return valid
	}

	// Phase 3: pair survivors with pipelined Lua scripts — 1 round-trip
	type pendingMatch struct {
		u1, u2  queuedUser
		matchID string
		result  *redis.Cmd
	}

	matchPipe := Client.Pipeline()
	var pending []pendingMatch

	for len(valid) >= 2 {
		u1 := valid[0]
		u2 := valid[1]
		valid = valid[2:]

		matchID := uuid.NewString()
		res := matchPipe.EvalSha(Ctx, matchScript.Hash(),
			[]string{
				"user_match:" + u1.userID,
				"user_match:" + u2.userID,
				"match:" + matchID,
			},
			matchID, u1.userID, u2.userID, time.Now().Unix(),
		)
		pending = append(pending, pendingMatch{u1, u2, matchID, res})
	}
	matchPipe.Exec(Ctx)

	// Phase 4: handle results, bulk-ACK paired, bulk-notify — 2 round-trips
	var leftover []queuedUser
	notifyPipe := Client.Pipeline()
	ackPipe2 := Client.Pipeline()

	for _, p := range pending {
		ackPipe2.XAck(Ctx, streamName, groupName, p.u1.msgID)
		ackPipe2.XAck(Ctx, streamName, groupName, p.u2.msgID)

		result, err := p.result.Int()
		if err != nil || result == 0 {
			leftover = append(leftover, p.u1, p.u2)
			continue
		}

		log.Printf("Matched: %s & %s (match:%s)\n", p.u1.userID, p.u2.userID, p.matchID)

		u1Msg, _ := json.Marshal(map[string]any{
			"client_id": p.u1.userID,
			"payload": map[string]any{
				"op": "match_found", "match_id": p.matchID, "role": "caller",
			},
		})
		u2Msg, _ := json.Marshal(map[string]any{
			"client_id": p.u2.userID,
			"payload": map[string]any{
				"op": "match_found", "match_id": p.matchID, "role": "callee",
			},
		})

		u1Server := servers[p.u1.userID]
		u2Server := servers[p.u2.userID]

		if u1Server != "" {
			notifyPipe.Publish(Ctx, "signal:"+u1Server, u1Msg)
		}
		if u2Server != "" {
			notifyPipe.Publish(Ctx, "signal:"+u2Server, u2Msg)
		}
	}

	ackPipe2.Exec(Ctx)
	notifyPipe.Exec(Ctx)

	// Single leftover user stays in buffer for next iteration
	if len(valid) == 1 {
		leftover = append(leftover, valid[0])
	}

	return leftover
}

func Requeue(userID string) {
	Client.XAdd(Ctx, &redis.XAddArgs{
		Stream: streamName,
		MaxLen: 10000,
		Values: map[string]interface{}{
			"client_id": userID,
		},
	})
}

/********************************
 * RATE LIMIT
 ********************************/

func CheckRateLimit(ip string, limit int, window time.Duration) (bool, error) {
	key := fmt.Sprintf("ratelimit:%s", ip)

	count, err := Client.Incr(Ctx, key).Result()
	if err != nil {
		return false, err
	}

	if count == 1 {
		Client.Expire(Ctx, key, window)
	}

	return count <= int64(limit), nil
}

func RefreshMatch(userID string) {
	matchID, err := Client.Get(Ctx, "user_match:"+userID).Result()
	if err != nil {
		return
	}
	pipe := Client.Pipeline()
	pipe.Expire(Ctx, "user_match:"+userID, 5*time.Minute)
	pipe.Expire(Ctx, "match:"+matchID, 5*time.Minute)
	pipe.Exec(Ctx)
}
