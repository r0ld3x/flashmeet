package middleware

import (
	"flashmeet/helper"
	"flashmeet/redis"
	"log"
	"net/http"
	"time"
)

func AllowHandshake(ip string, limit int) (bool, error) {
	allowed, err := redis.CheckRateLimit(ip, limit, 1*time.Minute)
	if err != nil {
		return false, err
	}
	return allowed, nil
}

func EnsureUpgradeChecks(w http.ResponseWriter, r *http.Request) bool {

	origin := r.Header.Get("Origin")
	// TODO: Add allowed origins
	if origin != "http://localhost:8080" && origin != "https://omiro.underthedesk.blog" {
		http.Error(w, "Forbidden origin", http.StatusForbidden)
		return false
	}

	ip := helper.GetRealIP(r)
	log.Printf("IP: %s", ip)
	ok, err := AllowHandshake(ip, 60)
	if err != nil || !ok {
		http.Error(w, "Too many requests", http.StatusTooManyRequests)
		return false
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-Session-Token")
	}
	log.Printf("Token: %s", token)
	if !VerifySessionToken(token) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	log.Printf("Token verified")
	return true
}
