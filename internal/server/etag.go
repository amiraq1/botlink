package server

import (
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
)

// weakETag derives a stable, cheap validator from a state fingerprint. The
// seed is built from the response fields (never from marshalled JSON), so a
// 304 costs no serialization.
func weakETag(seed string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	return `W/"` + strconv.FormatUint(h.Sum64(), 16) + `"`
}

// etagMatches implements If-None-Match comparison, including weak tags,
// comma-separated lists and "*".
func etagMatches(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(ifNoneMatch, ",") {
		candidate := strings.TrimSpace(part)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == want {
			return true
		}
	}
	return false
}

// writeJSONConditional writes v with an ETag. `private, no-cache` lets the
// browser store the body but revalidate on every use, so a 304 is returned
// without re-sending the payload; non-browser clients get the same 304.
func writeJSONConditional(w http.ResponseWriter, r *http.Request, seed string, v any) {
	etag := weakETag(seed)
	h := w.Header()
	h.Set("ETag", etag)
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "private, no-cache")
	}
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
