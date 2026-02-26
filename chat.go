package main

import (
	"encoding/json"
	"flashmeet/redis"
	"log"
)

func handleChat(c *Client, data json.RawMessage) {
	var payload struct {
		Message string `json:"message"`
	}
	target, err := getMatchTarget(c)
	if err != nil {
		log.Println("failed to get match target:", err)
		return
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		log.Println("chat payload invalid:", err)
		return
	}

	log.Printf("[%s] says: %s\n", c.ID, payload.Message)
	err = redis.SendToClient(target, map[string]any{
		"op":      "chat",
		"message": payload.Message,
	})
	if err != nil {
		log.Println("failed to forward chat message:", err)
	}

}
