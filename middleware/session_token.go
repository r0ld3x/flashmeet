package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var tokenSecret = []byte("replace-with-env-secret")

func GenerateSessionToken() (string, int64, error) {
	sid := uuid.NewString()
	//todo: add 10 minute timestamp and sign with HMAC
	exp := time.Now().Add(100 * time.Minute).Unix()
	msg := fmt.Sprintf("%s:%d", sid, exp)
	mac := hmac.New(sha256.New, tokenSecret)
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	token := fmt.Sprintf("%s:%s", msg, sig)
	return token, exp, nil
}

func ValidateSessionToken(token string) (string, int64, error) {
	parts := strings.Split(token, ":")
	if len(parts) != 2 {
		return "", 0, errors.New("invalid token")
	}
	msg := parts[0]
	sig := parts[1]
	mac := hmac.New(sha256.New, tokenSecret)
	mac.Write([]byte(msg))
	calculatedSig := hex.EncodeToString(mac.Sum(nil))
	if sig != calculatedSig {
		return "", 0, errors.New("invalid token")
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, errors.New("invalid token")
	}
	expTime := time.Unix(exp, 0)
	if time.Now().After(expTime) {
		return "", 0, errors.New("token expired")
	}
	return msg, expTime.Unix(), nil
}

func VerifySessionToken(token string) bool {
	parts := strings.Split(token, ":")
	if len(parts) != 3 {
		return false
	}
	sid, expStr, sig := parts[0], parts[1], parts[2]
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return false
	}
	if time.Now().Unix() > exp {
		return false
	}
	msg := fmt.Sprintf("%s:%s", sid, expStr)
	mac := hmac.New(sha256.New, tokenSecret)
	mac.Write([]byte(msg))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}
