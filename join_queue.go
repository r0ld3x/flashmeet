package main

import (
	redisClient "flashmeet/redis"

	"github.com/redis/go-redis/v9"
)

func handleJoinQueue(c *Client) {
	redisClient.Client.XAdd(redisClient.Ctx, &redis.XAddArgs{
		Stream: "matchmaking_stream",
		MaxLen: 10000,
		Values: map[string]interface{}{
			"client_id": c.ID,
		},
	})
}
