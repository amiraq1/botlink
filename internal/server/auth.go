package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookieName = "botlink_admin"
	sessionTTL        = 7 * 24 * time.Hour
)

// newSessionToken returns base64url(expiry) + "." + base64url(sessionID) +
// "." + base64url(HMAC-SHA256(secret, "botlink-admin-session|exp|id")).
// The id lets logout revoke the session in the database.
func newSessionToken(secret []byte, sessionID string, now time.Time) string {
	exp := now.Add(sessionTTL).Unix()
	payload := "botlink-admin-session|" + strconv.FormatInt(exp, 10) + "|" + sessionID
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(exp, 10))) +
		"." + base64.RawURLEncoding.EncodeToString([]byte(sessionID)) +
		"." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifySessionToken checks the signature and expiry and returns the session
// id for the database revocation check.
func verifySessionToken(secret []byte, token string, now time.Time) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	expBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	idBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", false
	}
	exp, err := strconv.ParseInt(string(expBytes), 10, 64)
	if err != nil || exp <= now.Unix() {
		return "", false
	}
	sessionID := string(idBytes)
	payload := "botlink-admin-session|" + strconv.FormatInt(exp, 10) + "|" + sessionID
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	if subtle.ConstantTimeCompare(sig, mac.Sum(nil)) != 1 {
		return "", false
	}
	return sessionID, true
}

// secureRequest only believes X-Forwarded-Proto when it came from a trusted
// proxy (or from the Fly edge, where trustHeaderAlways is enabled).
func (s *Server) secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !s.trustedPeer(r) && !s.trustHeaderAlways {
		return false
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string) {
	c := &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.secureRequest(r),
	}
	http.SetCookie(w, c)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.secureRequest(r),
	})
}

func (s *Server) isAuthenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	id, ok := verifySessionToken(s.sessionSecret, c.Value, time.Now())
	if !ok {
		return false
	}
	return s.store.IsAdminSessionValid(r.Context(), id, time.Now().UnixMilli())
}

// constantTimeEqual compares SHA-256 hashes of both inputs so neither the
// contents nor the length of the configured password affects timing.
func constantTimeEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// GenerateEphemeralSecret creates a fresh 32-byte session secret for dev
// runs where SESSION_SECRET is unset.
func GenerateEphemeralSecret() ([]byte, error) { return randomSecret() }

func randomSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func hexSecret(b []byte) string { return hex.EncodeToString(b) }
