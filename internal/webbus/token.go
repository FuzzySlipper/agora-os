package webbus

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Role string

const (
	RoleAgent Role = "agent"
	RoleHuman Role = "human"
)

type Identity struct {
	Role Role
	UID  uint32
}

type Claims struct {
	Role Role   `json:"role"`
	UID  uint32 `json:"uid"`
	Exp  int64  `json:"exp"`
}

func (c Claims) Identity() Identity {
	return Identity{Role: c.Role, UID: c.UID}
}

func (c Claims) Validate(now time.Time) error {
	switch c.Role {
	case RoleAgent:
		if c.UID == 0 {
			return errors.New("agent token requires non-zero uid")
		}
	case RoleHuman:
		if c.UID != 0 {
			return errors.New("human token must use uid 0")
		}
	default:
		return fmt.Errorf("unknown role %q", c.Role)
	}
	if c.Exp <= 0 {
		return errors.New("token missing exp")
	}
	if now.Unix() >= c.Exp {
		return errors.New("token expired")
	}
	return nil
}

func MintToken(secret []byte, claims Claims) (string, error) {
	if err := claims.Validate(time.Now()); err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig := sign(secret, encodedPayload)
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func VerifyToken(secret []byte, token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Claims{}, errors.New("invalid token format")
	}
	payloadPart, sigPart := parts[0], parts[1]
	expected := sign(secret, payloadPart)
	provided, err := base64.RawURLEncoding.DecodeString(sigPart)
	if err != nil {
		return Claims{}, fmt.Errorf("decode signature: %w", err)
	}
	if !hmac.Equal(expected, provided) {
		return Claims{}, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return Claims{}, fmt.Errorf("decode payload: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, fmt.Errorf("decode claims: %w", err)
	}
	if err := claims.Validate(now); err != nil {
		return Claims{}, err
	}
	return claims, nil
}

func LoadOrCreateSecret(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		if len(data) < 32 {
			return nil, fmt.Errorf("secret file %s is too short", path)
		}
		return data, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read secret file: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create secret dir: %w", err)
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	if err := os.WriteFile(path, secret, 0600); err != nil {
		return nil, fmt.Errorf("write secret file: %w", err)
	}
	return secret, nil
}

func sign(secret []byte, payload string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}
