package httputil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckExposure(t *testing.T) {
	tests := []struct {
		host, token string
		insecure    bool
		ok          bool
	}{
		{"127.0.0.1", "", false, true},
		{"localhost", "", false, true},
		{"::1", "", false, true},
		{"0.0.0.0", "", false, false},
		{"0.0.0.0", "s3cret", false, true},
		{"192.168.1.5", "", true, true},
	}
	for _, tt := range tests {
		if err := CheckExposure(tt.host, tt.token, tt.insecure); (err == nil) != tt.ok {
			t.Errorf("CheckExposure(%q, %q, %v) = %v", tt.host, tt.token, tt.insecure, err)
		}
	}
}

func TestRequireToken(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := RequireToken("s3cret", ok)
	for header, want := range map[string]int{
		"":              http.StatusUnauthorized,
		"Bearer wrong":  http.StatusUnauthorized,
		"Bearer s3cret": http.StatusOK,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Authorization %q: status %d, want %d", header, rec.Code, want)
		}
	}
	if RequireToken("", ok) == nil {
		t.Error("empty token should pass requests through")
	}
}
