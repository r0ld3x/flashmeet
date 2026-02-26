package main

import (
	"log"
	"sync"
	"time"

	"flashmeet/redis"

	"github.com/gorilla/websocket"
)

type Client struct {
	ID        string
	Conn      *websocket.Conn
	Send      chan SendMessageType
	done      chan struct{}
	closeOnce sync.Once
}

type SendMessageType struct {
	Message []byte
	Type    int
}

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		close(c.Send)
		c.Conn.Close()

		clientsMu.Lock()
		delete(clients, c.ID)
		clientsMu.Unlock()

		log.Println("client closed:", c.ID)
	})
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.Close()
	}()

	for {
		select {
		case <-c.done:
			return
		case msg, ok := <-c.Send:
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			err := c.Conn.WriteMessage(msg.Type, msg.Message)
			if err != nil {
				log.Printf("error writing message to %s: %s\n", c.ID, err)
				return
			}

		case <-ticker.C:
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("ping failed for %s: %s\n", c.ID, err)
				return
			}
		}
	}
}

func (c *Client) keepAlive(serverID string) {
	ticker := time.NewTicker(45 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			redis.RefreshClient(c.ID, serverID)
		}
	}
}
