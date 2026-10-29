// Package httputil holds small helpers shared by the controller, worker and chat client.
package httputil

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Getenv returns the environment variable key, or def if it is unset or empty.
func Getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// GetenvInt is Getenv for integers. It returns def if the variable is unset or invalid.
func GetenvInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return def
}

// GetenvBool is Getenv for booleans ("1", "true", "yes").
func GetenvBool(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// SplitList splits a comma-separated list, dropping blanks.
func SplitList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// WriteJSON writes v as a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}

// WriteError writes {"detail": msg}.
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

// RequireToken rejects requests without "Authorization: Bearer <token>". An empty token
// disables the check.
func RequireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			WriteError(w, http.StatusUnauthorized, "missing or invalid cluster token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SetToken adds the cluster token to an outgoing request, if there is one.
func SetToken(req *http.Request, token string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// IsLoopback reports whether host only accepts connections from this machine.
func IsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CheckExposure refuses to listen beyond this machine without a token, unless the
// caller explicitly opts out.
func CheckExposure(host, token string, insecure bool) error {
	if IsLoopback(host) || token != "" || insecure {
		return nil
	}
	return fmt.Errorf("refusing to listen on %s without a cluster token: set CLUSTER_TOKEN (or --token), "+
		"or pass --insecure if every machine that can reach this port is trusted", host)
}

// Serve serves handler on host:port until ctx is done, then shuts down gracefully.
func Serve(ctx context.Context, name, host string, port int, handler http.Handler) error {
	srv := &http.Server{
		Addr:              net.JoinHostPort(host, strconv.Itoa(port)),
		Handler:           logRequests(handler),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Printf("%s listening on http://%s", name, srv.Addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Printf("%s shutting down", name)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// DecodeResponse reads a JSON response into v, turning non-2xx statuses into StatusError.
func DecodeResponse(resp *http.Response, v any) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(data, v)
}

// StatusError is returned for a non-2xx response.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body) }

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush passes through so streaming handlers work behind the request logger.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %s %d", r.RemoteAddr, r.Method, r.URL.Path, rec.status)
	})
}
