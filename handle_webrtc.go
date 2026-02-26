package main

import (
	"encoding/json"
	"errors"
	"flashmeet/redis"
	"log"
)

func handleWebRTCOffer(c *Client, data json.RawMessage) {

	var payload struct {
		SDP string `json:"sdp"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		log.Println("invalid offer:", err)
		return
	}
	target, err := getMatchTarget(c)
	if err != nil {
		log.Println("failed to get match target:", err)
		return
	}
	// 4️⃣ Forward to partner
	err = redis.SendToClient(target, map[string]any{
		"op":   "webrtc_offer",
		"data": payload.SDP,
	})

	if err != nil {
		log.Println("failed to forward offer:", err)
	}
}

func handleWebRTCAnswer(c *Client, data json.RawMessage) {

	var payload struct {
		SDP string `json:"sdp"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		log.Println("invalid answer:", err)
		return
	}

	target, err := getMatchTarget(c)
	if err != nil {
		log.Println("failed to get match target:", err)
		return
	}

	outgoing := map[string]any{
		"op": "webrtc_answer",
		"data": map[string]any{
			"from": c.ID,
			"sdp":  payload.SDP,
		},
	}

	err = redis.SendToClient(target, outgoing)
	if err != nil {
		log.Println("failed to forward answer:", err)
	}
}

func handleICECandidate(c *Client, data json.RawMessage) {

	var payload struct {
		Candidate map[string]any `json:"candidate"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		log.Println("invalid ice candidate:", err)
		return
	}

	target, err := getMatchTarget(c)
	if err != nil {
		log.Println("failed to get match target:", err)
		return
	}

	outgoing := map[string]any{
		"op": "ice_candidate",
		"data": map[string]any{
			"from":      c.ID,
			"candidate": payload.Candidate,
		},
	}

	err = redis.SendToClient(target, outgoing)
	if err != nil {
		log.Println("failed to forward ice candidate:", err)
	}
}

func getMatchTarget(c *Client) (string, error) {
	matchID, err := redis.Client.Get(redis.Ctx, "user_match:"+c.ID).Result()
	if err != nil {
		return "", err
	}

	match, err := redis.Client.HGetAll(redis.Ctx, "match:"+matchID).Result()
	if err != nil || len(match) == 0 {
		return "", err
	}

	if match["u1"] == c.ID {
		return match["u2"], nil
	} else if match["u2"] == c.ID {
		return match["u1"], nil
	}

	return "", errors.New("client not part of match")
}

func delPartnerRelationship(c *Client) {
	matchID, err := redis.Client.Get(redis.Ctx, "user_match:"+c.ID).Result()
	if err != nil {
		return
	}

	matchKey := "match:" + matchID

	match, err := redis.Client.HGetAll(redis.Ctx, matchKey).Result()
	if err != nil || len(match) == 0 {
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

	redis.Client.Del(redis.Ctx, "user_match:"+c.ID)
	redis.Client.Del(redis.Ctx, "user_match:"+partnerID)
	redis.Client.Del(redis.Ctx, matchKey)
}
