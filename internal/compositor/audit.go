package compositor

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

func generateSessionToken() string {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(buf[:])
}

func (b *Bridge) requireSessionToken(sessionID, token string) error {
	if sessionID == "" {
		return nil
	}
	b.mu.RLock()
	session, ok := b.sessions[sessionID]
	b.mu.RUnlock()
	if !ok {
		return compositorError(schema.ErrorSessionNotFound, "session %s not found", sessionID)
	}
	if session.SessionToken == "" || token == "" || token != session.SessionToken {
		return compositorError(schema.ErrorSessionTokenRequired, "valid session_token is required for session %s", sessionID)
	}
	return nil
}

func (b *Bridge) requireLaunchSessionToken(launchID, token string) error {
	b.mu.RLock()
	launch := b.launches[launchID]
	b.mu.RUnlock()
	if launch == nil || launch.process.SessionID == "" {
		return nil
	}
	return b.requireSessionToken(launch.process.SessionID, token)
}

func (b *Bridge) sessionAuditCorrelation(sessionID, explicit string) string {
	if explicit != "" || sessionID == "" {
		return explicit
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if session, ok := b.sessions[sessionID]; ok {
		return session.AuditCorrelationID
	}
	return ""
}

func (b *Bridge) touchSession(sessionID string) {
	if sessionID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	session, ok := b.sessions[sessionID]
	if !ok {
		return
	}
	session.LastUsedAt = time.Now()
	b.sessions[sessionID] = session
}

func (b *Bridge) authorizeSurfaceSession(surfaceID, requestedSessionID, token string) (string, error) {
	surface, err := b.GetSurface(surfaceID)
	if err != nil {
		return "", err
	}
	targetSessionID := surface.SessionID
	if targetSessionID == "" {
		targetSessionID = requestedSessionID
	}
	if requestedSessionID != "" && surface.SessionID != "" && requestedSessionID != surface.SessionID {
		return "", compositorError(schema.ErrorSessionTokenRequired, "surface %s belongs to session %s, not %s", surfaceID, surface.SessionID, requestedSessionID)
	}
	if targetSessionID != "" {
		if err := b.requireSessionToken(targetSessionID, token); err != nil {
			return "", err
		}
	}
	return targetSessionID, nil
}
