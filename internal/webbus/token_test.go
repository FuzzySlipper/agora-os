package webbus

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenRoundTrip(t *testing.T) {
	t.Parallel()

	secret := []byte("01234567890123456789012345678901")
	now := time.Now().UTC().Truncate(time.Second)
	claims := Claims{Role: RoleAgent, UID: 60001, Exp: now.Add(time.Hour).Unix()}
	token, err := MintToken(secret, claims)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	got, err := VerifyToken(secret, token, now)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if got.Role != claims.Role || got.UID != claims.UID || got.Exp != claims.Exp {
		t.Fatalf("got %+v, want %+v", got, claims)
	}
}

func TestVerifyTokenRejectsTampering(t *testing.T) {
	t.Parallel()

	secret := []byte("01234567890123456789012345678901")
	now := time.Now().UTC().Truncate(time.Second)
	claims := Claims{Role: RoleHuman, UID: 0, Exp: now.Add(time.Hour).Unix()}
	token, err := MintToken(secret, claims)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	token += "x"
	if _, err := VerifyToken(secret, token, now); err == nil {
		t.Fatal("expected tampered token to fail verification")
	}
}

func TestLoadOrCreateSecretCreatesParentDirAndReusesSecret(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "event-bus-web.secret")
	first, err := LoadOrCreateSecret(path)
	if err != nil {
		t.Fatalf("LoadOrCreateSecret create: %v", err)
	}
	if len(first) != 32 {
		t.Fatalf("got secret len %d, want 32", len(first))
	}

	second, err := LoadOrCreateSecret(path)
	if err != nil {
		t.Fatalf("LoadOrCreateSecret reload: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("secret changed across reload")
	}
}
