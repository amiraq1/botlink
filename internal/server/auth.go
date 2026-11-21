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

// newSessionToken returns a signed session value: base64url(expiryUnix) + "."
// + base64url(HMAC-SHA256(secret, "botlink-admin-session|<expiryUnix>")).
func newSessionToken(secret []byte, now time.Time) string {
	exp := now.Add(sessionTTL).Unix()
	payload := "botlink-admin-session|" + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(exp, 10))) +
		"." + base64.RawURLEncoding.EncodeToString(sig)
}

func verifySessionToken(secret []byte, token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	expBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	exp, err := strconv.ParseInt(string(expBytes), 10, 64)
	if err != nil || exp <= now.Unix() {
		return false
	}
	payload := "botlink-admin-session|" + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	expected := mac.Sum(nil)
	return subtle.ConstantTimeCompare(sig, expected) == 1
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, secret []byte, now time.Time) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	c := &http.Cookie{
		Name:     sessionCookieName,
		Value:    newSessionToken(secret, now),
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	}
	http.SetCookie(w, c)
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) isAuthenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return verifySessionToken(s.sessionSecret, c.Value, time.Now())
}

// constantTimeEqual compares the supplied password with the configured admin
// password in constant time.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		// Still hash both sides so the timing does not leak the length alone.
		ha := sha256.Sum256([]byte(a))
		hb := sha256.Sum256([]byte(b))
		return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1 && false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
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
