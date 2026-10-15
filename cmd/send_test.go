package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// escapeCmd coverage: literal \r \n \t replacement, ordering, and pass-through when no escapes are present.
func TestEscapeCmd(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty string", "", ""},
		{"no escapes, passed through", "AT+OPEN", "AT+OPEN"},
		{"single carriage return", `AT\r`, "AT\r"},
		{"single newline", `AT\n`, "AT\n"},
		{"single tab", `a\tb`, "a\tb"},
		{"combined CRLF", `AT+OPEN\r\n`, "AT+OPEN\r\n"},
		{"multiple mixed groups", `\r\n\t\r\n`, "\r\n\t\r\n"},
		{"real control chars left alone", "AT\r\n", "AT\r\n"},
		{"embedded in the middle", "ls\r-al", "ls\r-al"},
		{"unknown escapes untouched", `a\rb`, "a\rb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeCmd(tc.in); got != tc.want {
				t.Errorf("escapeCmd(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// remoteWriter.Send success path: 2xx (200) returns nil, and the form body carries the data field.
func TestRemoteWriterSendSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/write" {
			t.Errorf("request path = %q, want /api/write", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("request method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		if !strings.Contains(string(body), "data="+urlQueryEscape("AT\r\n")) {
			t.Errorf("request body = %q, missing data field", string(body))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := &remoteWriter{base: srv.URL, cli: srv.Client()}
	if err := w.Send(context.Background(), []byte("AT\r\n")); err != nil {
		t.Fatalf("Send returned an error: %v", err)
	}
}

// urlQueryEscape is a test-only helper: it matches the + encoding used by url.Values.Encode.
func urlQueryEscape(s string) string {
	r := strings.NewReplacer(" ", "+", "\r", "%0D", "\n", "%0A")
	return r.Replace(s)
}

// remoteWriter.Send non-2xx: a 500 from the agent should error, with the status text in the message.
func TestRemoteWriterSendNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := &remoteWriter{base: srv.URL, cli: srv.Client()}
	err := w.Send(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("expected a non-nil error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not contain status code 500", err)
	}
}

// remoteWriter.Send with token: the request must carry Bearer when a token is set; no Authorization header when empty.
func TestRemoteWriterSendToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := &remoteWriter{base: srv.URL, token: "secret-token", cli: srv.Client()}
	if err := w.Send(context.Background(), []byte("x")); err != nil {
		t.Fatalf("Send returned an error: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-token")
	}

	// Without a token: no Authorization header should appear
	gotAuth = "unset"
	w2 := &remoteWriter{base: srv.URL, cli: srv.Client()}
	if err := w2.Send(context.Background(), []byte("x")); err != nil {
		t.Fatalf("Send returned an error: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("without token, Authorization = %q, want empty", gotAuth)
	}
}

// remoteWriter.Send unreachable network: a failed client Do should return a wrapped error.
func TestRemoteWriterSendRequestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately so the request is guaranteed to fail

	w := &remoteWriter{base: srv.URL, cli: srv.Client()}
	err := w.Send(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("expected a non-nil error, got nil")
	}
	if !strings.Contains(err.Error(), "sending request") {
		t.Errorf("error %q does not contain the wrapper prefix", err)
	}
}
