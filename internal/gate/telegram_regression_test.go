package gate

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type gateTransport func(*http.Request) (*http.Response, error)

func (f gateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramTransportErrorDoesNotExposeCredentials(t *testing.T) {
	t.Parallel()
	token := "SENTINEL-BOT-TOKEN"
	err := &url.Error{Op: "Get", URL: "https://example.com/bot" + token + "/getUpdates", Err: errors.New("failure " + token)}
	if got := telegramTransportError("poll", err).Error(); strings.Contains(got, token) {
		t.Fatal("credential exposed")
	}
}

func TestCloseDrainsBlockedSend(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	entered := make(chan struct{})
	exited := make(chan struct{})
	transport := gateTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			close(entered)
			<-r.Context().Done()
			close(exited)
			return nil, r.Context().Err()
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	g, err := NewTelegramGate("test", []byte(testBotToken), testChatID, testUserID, time.Minute, time.Minute, nil, WithAPIBase(ft.server.URL), func(g *TelegramGate) { g.httpClient.Transport = transport })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	if _, err := checkDevice(t, g); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("send did not start")
	}
	done := make(chan struct{})
	go func() { _ = g.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel send")
	}
	select {
	case <-exited:
	default:
		t.Fatal("Close did not drain sender")
	}
	if d, err := g.Check(context.Background(), Device{Name: testDeviceName, UUID: testUUID}); err != nil || d != Denied {
		t.Fatalf("closed gate: %v, %v", d, err)
	}
}

func TestCallbackCannotApproveReplacementRequest(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	g := newTestGate(t, ft, time.Minute, time.Minute, nil)
	if _, err := checkDevice(t, g); err != nil {
		t.Fatal(err)
	}
	waitForApprovalMessage(t, ft)
	encoded := ft.sentApprovals()[0]
	if len(encoded) > 64 {
		t.Fatalf("callback length %d", len(encoded))
	}
	data, err := g.decodeCallbackData(encoded)
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	replacement := g.pendingRequests[testUUID]
	replacement.createdAt = replacement.createdAt.Add(time.Nanosecond)
	g.pendingRequests[testUUID] = replacement
	g.mu.Unlock()
	g.processCallback(data)
	g.mu.RLock()
	defer g.mu.RUnlock()
	if _, ok := g.approvedCache[testUUID]; ok {
		t.Fatal("old callback approved replacement")
	}
	if _, ok := g.pendingRequests[testUUID]; !ok {
		t.Fatal("old callback removed replacement")
	}
}
