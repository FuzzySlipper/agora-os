package webbus

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func AuthenticateRequest(secret []byte, now time.Time, r *http.Request) (Identity, string, error) {
	token, selectedSubprotocol := tokenFromRequest(r)
	if token == "" {
		return Identity{}, "", errors.New("missing bearer token")
	}
	claims, err := VerifyToken(secret, token, now)
	if err != nil {
		return Identity{}, "", err
	}
	return claims.Identity(), selectedSubprotocol, nil
}

func CheckOrigin(allowedOrigins map[string]struct{}, r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	if len(allowedOrigins) > 0 {
		_, ok := allowedOrigins[origin]
		return ok
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func tokenFromRequest(r *http.Request) (string, string) {
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(authz, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authz, "Bearer ")), ""
	}
	for _, protocol := range websocket.Subprotocols(r) {
		if strings.HasPrefix(protocol, tokenSubprotocolPrefix) {
			return strings.TrimPrefix(protocol, tokenSubprotocolPrefix), protocol
		}
	}
	return "", ""
}
