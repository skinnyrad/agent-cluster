// Package httputil holds small helpers shared by the controller and worker servers.
package httputil

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
)

// Getenv returns the environment variable key, or def if it is unset or empty.
func Getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// WriteJSON writes v as a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}

// WriteError writes {"detail": msg}, matching FastAPI's error shape.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"detail": msg})
}

// Validator is implemented by request bodies that can check themselves.
type Validator interface {
	Validate() error
}

// ReadJSON decodes the request body into v and validates it. On failure it writes a 422
// response and returns false.
func ReadJSON(w http.ResponseWriter, r *http.Request, v Validator) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "invalid JSON body: "+err.Error())
		return false
	}
	if err := v.Validate(); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, err.Error())
		return false
	}
	return true
}

// ListenFlags registers --host and --port flags, mirroring uvicorn's.
func ListenFlags(defaultPort int) (host *string, port *int) {
	host = flag.String("host", "0.0.0.0", "interface to listen on")
	port = flag.Int("port", defaultPort, "port to listen on")
	return host, port
}

// ListenAndServe serves handler on host:port and logs requests.
func ListenAndServe(name, host string, port int, handler http.Handler) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	log.Printf("%s listening on http://%s", name, addr)
	err := http.ListenAndServe(addr, logRequests(handler))
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %s %d", r.RemoteAddr, r.Method, r.URL.Path, rec.status)
	})
}
