package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const gzipMinSize = 512

var gzipWriters = sync.Pool{
	New: func() any {
		// BestSpeed keeps the added latency far below the byte savings.
		w, err := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		if err != nil {
			return nil
		}
		return w
	},
}

// gzipMiddleware compresses textual responses. It buffers the first bytes so
// tiny JSON payloads stay uncompressed, and it never touches event streams,
// HEAD requests or range responses.
func (s *Server) gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gzipWanted(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.Close()
		next.ServeHTTP(gw, r)
	})
}

func gzipWanted(r *http.Request) bool {
	if r.Method == http.MethodHead {
		return false
	}
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return false
	}
	if r.Header.Get("Range") != "" {
		return false
	}
	p := r.URL.Path
	return p != "/healthz" && !strings.HasSuffix(p, "/events")
}

func gzipCompressible(contentType string) bool {
	if strings.HasPrefix(contentType, "text/event-stream") {
		return false
	}
	switch {
	case strings.HasPrefix(contentType, "application/json"),
		strings.HasPrefix(contentType, "text/"),
		strings.HasPrefix(contentType, "application/javascript"),
		strings.HasPrefix(contentType, "application/x-javascript"),
		strings.HasPrefix(contentType, "image/svg+xml"):
		return true
	}
	return false
}

type gzipResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	decided     bool
	compress    bool
	buf         []byte
	gz          *gzip.Writer
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	if g.wroteHeader {
		return
	}
	g.status = status
	g.wroteHeader = true
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.decided {
		if g.compress {
			if _, err := g.gz.Write(p); err != nil {
				return 0, err
			}
			return len(p), nil
		}
		return g.ResponseWriter.Write(p)
	}
	g.buf = append(g.buf, p...)
	if len(g.buf) < gzipMinSize {
		return len(p), nil
	}
	if err := g.decide(); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (g *gzipResponseWriter) decide() error {
	g.decided = true
	h := g.ResponseWriter.Header()
	bodyAllowed := g.status >= 200 && g.status != http.StatusNoContent && g.status != http.StatusNotModified
	g.compress = bodyAllowed &&
		h.Get("Content-Encoding") == "" &&
		gzipCompressible(h.Get("Content-Type"))
	if !g.compress {
		if bodyAllowed && h.Get("Content-Length") == "" {
			h.Set("Content-Length", strconv.Itoa(len(g.buf)))
		}
		g.ResponseWriter.WriteHeader(g.status)
		_, err := g.ResponseWriter.Write(g.buf)
		g.buf = nil
		return err
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	gz := gzipWriters.Get().(*gzip.Writer)
	if gz == nil {
		g.compress = false
		return nil
	}
	gz.Reset(g.ResponseWriter)
	g.gz = gz
	g.ResponseWriter.WriteHeader(g.status)
	_, err := g.gz.Write(g.buf)
	g.buf = nil
	return err
}

func (g *gzipResponseWriter) Flush() {
	if !g.decided {
		_ = g.decide()
	}
	if g.compress {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipResponseWriter) Close() {
	if !g.decided {
		if g.status == 0 {
			g.status = http.StatusOK
		}
		g.decided = true
		h := g.ResponseWriter.Header()
		if g.status >= 200 && g.status != http.StatusNoContent && g.status != http.StatusNotModified && h.Get("Content-Length") == "" {
			h.Set("Content-Length", strconv.Itoa(len(g.buf)))
		}
		g.ResponseWriter.WriteHeader(g.status)
		if len(g.buf) > 0 {
			_, _ = g.ResponseWriter.Write(g.buf)
		}
		g.buf = nil
		return
	}
	if g.compress {
		_ = g.gz.Close()
		gzipWriters.Put(g.gz)
		g.gz = nil
	}
}
