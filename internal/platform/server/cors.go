package server

import "net/http"

// CORS returns a middleware that allows cross-origin browser requests
// from the listed origins. Mount it on the root router so preflights
// are answered before routing. Hand-written on purpose: the API uses
// Bearer tokens, not cookies, so credentialed CORS — the part worth a
// library — never applies, and the whole policy fits in one screen.
//
// Behaviour: a request without an Origin header passes through
// untouched. An allowed origin gets Access-Control-Allow-Origin plus
// Vary: Origin; its preflight (OPTIONS with Access-Control-Request-
// Method) is answered 204 with the method/header allowances below. An
// origin outside the allowlist is never reflected — the browser blocks
// the response on its side.
func CORS(origins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			if _, ok := allowed[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")

				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
					w.Header().Set("Access-Control-Max-Age", "300")
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
