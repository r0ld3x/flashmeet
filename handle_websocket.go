package main

import (
	"encoding/json"
	"flashmeet/middleware"
	"flashmeet/redis"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var clients = make(map[string]*Client)
var clientsMu sync.RWMutex

const maxConns = 50000

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	clientsMu.RLock()
	count := len(clients)
	clientsMu.RUnlock()
	if count >= maxConns {
		http.Error(w, "Server at capacity", http.StatusServiceUnavailable)
		return
	}
	if !middleware.EnsureUpgradeChecks(w, r) {
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade error:", err)
		return
	}
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	defer conn.Close()
	log.Println("client connected:", conn.RemoteAddr())

	client := &Client{
		ID:   uuid.NewString(),
		Conn: conn,
		Send: make(chan SendMessageType, 256),
		done: make(chan struct{}),
	}

	clientsMu.Lock()
	clients[client.ID] = client
	clientsMu.Unlock()

	ip := r.RemoteAddr
	err = redis.RegisterClient(client.ID, ip, serverID)
	defer func() {
		clientsMu.Lock()
		delete(clients, client.ID)
		clientsMu.Unlock()
		redis.Client.Del(redis.Ctx, "client:"+client.ID)
	}()

	if err != nil {
		log.Println("failed to register client in redis:", err)
		return
	}

	go client.writePump()
	go client.readPump()
	go client.keepAlive(serverID)

	welcomeMsg := map[string]any{
		"message":   "Hello from server",
		"timestamp": time.Now().Unix(),
	}

	msgBytes, err := json.Marshal(welcomeMsg)
	if err != nil {
		log.Println("json marshal error:", err)
		return
	}

	client.Send <- SendMessageType{
		Message: msgBytes,
		Type:    websocket.TextMessage,
	}

	<-client.done
	redis.Client.Del(redis.Ctx, "client:"+client.ID)
	log.Println("handleWebSocket exiting for:", client.ID)
}
