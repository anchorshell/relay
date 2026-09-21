package transport

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/testutil"
)

func TestAuthorizationRemovedForAuthModeNone(t *testing.T) {
	var gotAuth string
	transport := &UpstreamTransport{
		Base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotAuth = r.Header.Get("Authorization")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		}),
		Store:   testutil.NewStore(t),
		Learner: NewObservedLearner(testutil.NewStore(t), nil),
	}
	req, _ := http.NewRequest(http.MethodGet, "https://upstream.example.test", nil)
	req.Header.Set("Authorization", "Bearer remove-me")
	req = req.WithContext(WithRequestContext(context.Background(), RequestContext{
		Provider: models.Provider{AuthMode: "none"},
		Endpoint: models.Endpoint{ID: 1},
	}))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if gotAuth != "" {
		t.Fatalf("expected auth stripped, got %q", gotAuth)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRetryAfterSetsCooldown(t *testing.T) {
	st := testutil.NewStore(t)
	learner := NewObservedLearner(st, nil)
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true}
	if err := st.Create(context.Background(), &endpoint); err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	headers.Set("Retry-After", "10")
	if err := learner.Learn(context.Background(), endpoint, headers, http.StatusTooManyRequests); err != nil {
		t.Fatal(err)
	}
	var updated models.Endpoint
	if err := st.FindByID(context.Background(), &updated, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if updated.CooldownUntil == nil {
		t.Fatal("expected cooldown to be set")
	}
	if updated.CooldownReason != "upstream_rate_limited" || updated.CooldownStatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected stored retry cause, got reason=%q status=%d", updated.CooldownReason, updated.CooldownStatusCode)
	}
}
