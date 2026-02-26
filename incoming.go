package main

import (
	"encoding/json"
	"flashmeet/redis"
	"log"
)

func (c *Client) readPump() {
	defer func() {
		handleClientDisconnect(c)
	}()

	for {
		_, msg, err := c.Conn.ReadMessage()
		if err != nil {
			return
		}

		var incoming struct {
			Op   string          `json:"op"`
			Data json.RawMessage `json:"data"`
		}

		if err := json.Unmarshal(msg, &incoming); err != nil {
			log.Println("json unmarshal error:", err)
			continue
		}

		routeMessage(c, incoming.Op, incoming.Data)
	}
}

func routeMessage(c *Client, op string, data json.RawMessage) {
	switch op {

	case "join_queue":
		handleJoinQueue(c)

	case "chat":
		handleChat(c, data)

	case "disconnect":
		handleClientDisconnect(c)

	case "next":
		handleNextPartner(c)

	case "webrtc_offer":
		handleWebRTCOffer(c, data)

	case "webrtc_answer":
		handleWebRTCAnswer(c, data)

	case "ice_candidate":
		handleICECandidate(c, data)

	default:
		log.Println("unknown op:", op)
	}
}

func handleClientDisconnect(c *Client) {
	c.Close()

	redis.Client.Del(redis.Ctx, "client:"+c.ID)

	matchID, err := redis.Client.Get(redis.Ctx, "user_match:"+c.ID).Result()
	if err != nil {
		return
	}

	matchKey := "match:" + matchID

	match, err := redis.Client.HGetAll(redis.Ctx, matchKey).Result()
	if err != nil || len(match) == 0 {
		redis.Client.Del(redis.Ctx, "user_match:"+c.ID)
		return
	}

	var partnerID string
	if match["u1"] == c.ID {
		partnerID = match["u2"]
	} else if match["u2"] == c.ID {
		partnerID = match["u1"]
	} else {
		return
	}
	pipe := redis.Client.Pipeline()
	pipe.Del(redis.Ctx, "user_match:"+c.ID)
	pipe.Del(redis.Ctx, "user_match:"+partnerID)
	pipe.Del(redis.Ctx, matchKey)
	_, err = pipe.Exec(redis.Ctx)
	if err != nil {
		log.Println("failed to delete match:", err)

	}
	partnerAlive := redis.Client.Exists(redis.Ctx, "client:"+partnerID).Val() == 1

	if partnerAlive {
		redis.SendToClient(partnerID, map[string]any{
			"op": "partner_disconnected",
		})
		redis.Requeue(partnerID)
	}
}

func handleNextPartner(c *Client) {
	log.Println("client looking for next partner:", c.ID)

	target, err := getMatchTarget(c)
	if err != nil {
		log.Println("failed to get match target:", err)
		return
	}
	delPartnerRelationship(c)

	notifyPartnerLeft(target)

	handleJoinQueue(c)
	redis.Requeue(target)

	log.Printf("client %s re-queued for next match\n", c.ID)
}

func forwardWebRTC(c *Client, op string, matchID string, payload json.RawMessage) {

	match, err := redis.Client.HGetAll(redis.Ctx, "match:"+matchID).Result()
	if err != nil || len(match) == 0 {
		return
	}

	var target string

	if match["u1"] == c.ID {
		target = match["u2"]
	} else if match["u2"] == c.ID {
		target = match["u1"]
	} else {
		return // sender not part of match
	}

	redis.SendToClient(target, map[string]any{
		"op":       op,
		"match_id": matchID,
		"data":     payload,
	})
}
func notifyPartnerLeft(userID string) {
	redis.SendToClient(userID, map[string]any{
		"op": "partner_disconnected",
	})
}
