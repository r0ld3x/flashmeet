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

// This must be called inside EACH WS server, passing its serverID AND a handler
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

type PairResult int

const (
	PairCreated PairResult = iota
	RequeueU1
	RequeueU2
	DropBoth
)

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
						fmt.Printf("Matchmaker auto-claimed message: %v\n", msg)
						defer func() {
							if r := recover(); r != nil {
								log.Printf("PANIC while processing message %s: %v", msg.ID, r)
								Client.XAck(Ctx, streamName, groupName, msg.ID)
								if err != nil {
									log.Printf("Failed to ACK after panic: %v", err)
								}
							}
						}()

						buffer = append(buffer, queuedUser{
							userID: msg.Values["client_id"].(string),
							msgID:  msg.ID,
						})
					}(msg)
				}
			}

		default:
			res, err := Client.XReadGroup(Ctx, &redis.XReadGroupArgs{
				Group:    groupName,
				Consumer: serverId,
				Streams:  []string{streamName, ">"},
				Count:    1,
				Block:    time.Duration(15) * time.Second,
			}).Result()

			if err == nil {
				for _, stream := range res {
					for _, msg := range stream.Messages {
						fmt.Printf("Matchmaker received message: %v\n", msg)
						buffer = append(buffer, queuedUser{
							userID: msg.Values["client_id"].(string),
							msgID:  msg.ID,
						})
					}
				}
			}
		}
		log.Printf("buffer: %+v\n ", buffer)
		for len(buffer) >= 2 {

			u1 := buffer[0]
			u2 := buffer[1]
			buffer = buffer[2:]

			result := createPair(u1, u2)

			switch result {

			case PairCreated:
				Client.XAck(Ctx, streamName, groupName, u1.msgID)
				Client.XAck(Ctx, streamName, groupName, u2.msgID)

			case RequeueU1:
				requeue(u1.userID)
				Client.XAck(Ctx, streamName, groupName, u1.msgID)
				Client.XAck(Ctx, streamName, groupName, u2.msgID)

			case RequeueU2:
				requeue(u2.userID)
				Client.XAck(Ctx, streamName, groupName, u1.msgID)
				Client.XAck(Ctx, streamName, groupName, u2.msgID)

			case DropBoth:
				Client.XAck(Ctx, streamName, groupName, u1.msgID)
				Client.XAck(Ctx, streamName, groupName, u2.msgID)
			}
		}
	}
}

// TODO: client has only 60 seconds update it
func createPair(u1, u2 queuedUser) PairResult {

	// Check presence
	_, err1 := GetClient(u1.userID)
	_, err2 := GetClient(u2.userID)

	alive1 := err1 == nil
	alive2 := err2 == nil

	// Prevent double matching
	if alive1 {
		if _, err := Client.Get(Ctx, "user_match:"+u1.userID).Result(); err == nil {
			alive1 = false
		}
	}

	if alive2 {
		if _, err := Client.Get(Ctx, "user_match:"+u2.userID).Result(); err == nil {
			alive2 = false
		}
	}

	switch {

	case alive1 && alive2:
		log.Printf("Creating pair: %s & %s\n", u1.userID, u2.userID)
		matchID := uuid.NewString()

		err := Client.HSet(Ctx, "match:"+matchID,
			"u1", u1.userID,
			"u2", u2.userID,
			"u1_connected", 1,
			"u2_connected", 1,
			"created_at", time.Now().Unix(),
		).Err()

		if err != nil {
			log.Printf("Failed to create match: %v", err)
			return DropBoth
		}

		Client.Expire(Ctx, "match:"+matchID, 5*time.Minute)

		lock1, _ := Client.SetNX(Ctx, "user_match:"+u1.userID, matchID, 5*time.Minute).Result()
		if !lock1 {
			return RequeueU2
		}

		lock2, _ := Client.SetNX(Ctx, "user_match:"+u2.userID, matchID, 5*time.Minute).Result()
		if !lock2 {
			Client.Del(Ctx, "user_match:"+u1.userID)
			return RequeueU1
		}

		SendToClient(u1.userID, map[string]any{
			"op":       "match_found",
			"match_id": matchID,
			"role":     "caller",
		})

		SendToClient(u2.userID, map[string]any{
			"op":       "match_found",
			"match_id": matchID,
			"role":     "callee",
		})

		return PairCreated

	case alive1 && !alive2:
		return RequeueU1

	case !alive1 && alive2:
		return RequeueU2

	default:
		return DropBoth
	}
}

func requeue(userID string) {
	Client.XAdd(Ctx, &redis.XAddArgs{
		Stream: "matchmaking_stream",
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

func RefreshClient(clientID, serverID string) {
	key := fmt.Sprintf(clientKey, clientID)
	Client.Set(Ctx, key, serverID, 60*time.Second)
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
