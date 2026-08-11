package pocketbase

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestDoRefreshesTokenAndPreservesPayload(t *testing.T) {
	var calls atomic.Int32
	var payloads [][]byte
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/auth-with-password") {
			return response(http.StatusOK, `{"token":"fresh"}`), nil
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, body)
		if calls.Add(1) == 1 {
			return response(http.StatusUnauthorized, `{"message":"expired"}`), nil
		}
		if got := request.Header.Get("Authorization"); got != "Bearer fresh" {
			t.Fatalf("Authorization = %q", got)
		}
		return response(http.StatusOK, `{"ok":true}`), nil
	})
	client := New("http://pocketbase.test/", "admin@example.test", "secret", &http.Client{Transport: transport})

	body, err := client.Do(http.MethodPost, "http://pocketbase.test/api/test", []byte(`{"job":"one"}`))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("Do() body = %s", body)
	}
	if len(payloads) != 2 || !bytes.Equal(payloads[0], payloads[1]) {
		t.Fatalf("retried payloads = %q", payloads)
	}
}

func TestDoReturnsTypedStatusError(t *testing.T) {
	client := New("http://pocketbase.test", "admin", "secret", &http.Client{Transport: roundTripFunc(
		func(_ *http.Request) (*http.Response, error) {
			return response(http.StatusInternalServerError, "database unavailable"), nil
		},
	)})

	_, err := client.Do(http.MethodGet, "http://pocketbase.test/api/fail", nil)
	statusErr, ok := err.(*StatusError)
	if !ok || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("Do() error = %#v", err)
	}
}

func TestLimitedBodyBoundsDiagnostics(t *testing.T) {
	body := []byte(strings.Repeat("x", maxResponseBody+10))
	limited := LimitedBody(body)
	if !strings.HasSuffix(limited, "…") || len([]byte(strings.TrimSuffix(limited, "…"))) != maxResponseBody {
		t.Fatalf("LimitedBody() length = %d", len(limited))
	}
}
