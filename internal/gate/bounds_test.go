package gate

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestConcurrentSendLimit(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	entered := make(chan struct{}, maxConcurrentSends)
	transport := gateTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			entered <- struct{}{}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	g, err := NewTelegramGate("test", []byte(testBotToken), testChatID, testUserID, time.Minute, 0, nil, WithAPIBase(f.server.URL), func(g *TelegramGate) { g.httpClient.Transport = transport })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	results := make(chan Decision, maxConcurrentSends)
	for i := range maxConcurrentSends {
		go func() {
			d, _ := g.Check(t.Context(), Device{UUID: fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", i)})
			results <- d
		}()
	}
	for range maxConcurrentSends {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("send did not start")
		}
	}
	if d, _ := checkDevice(t, g); d != Denied {
		t.Fatal("concurrent send bound not enforced")
	}
	_ = g.Close()
	for range maxConcurrentSends {
		awaitDecision(t, results, Denied)
	}
}

func TestPendingAndRetainedStateLimits(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"pending", "rate state", "cache state"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newFakeTelegram(t)
			g := newTestGate(t, f, time.Minute, 0, nil)
			g.mu.Lock()
			switch kind {
			case "pending":
				for i := range maxPendingRequests {
					g.pendingRequests[fmt.Sprint(i)] = nil
				}
			case "rate state":
				g.promptInterval = time.Hour
				for i := range maxTrackedDevices {
					g.lastPrompt[fmt.Sprint(i)] = time.Now()
				}
			case "cache state":
				for i := range maxTrackedDevices {
					g.approvedCache[fmt.Sprint(i)] = approvedCacheEntry{expiresAt: time.Now().Add(time.Hour)}
				}
			}
			g.mu.Unlock()
			if d, _ := checkDevice(t, g); d != Denied {
				t.Fatal("state bound not enforced")
			}
			g.mu.Lock()
			clear(g.pendingRequests)
			g.mu.Unlock()
			if len(f.sentApprovals()) != 0 {
				t.Fatal("overload sent a prompt")
			}
		})
	}
}

func TestCacheExpiryAndDeviceScope(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	g := newTestGate(t, f, time.Minute, time.Minute, nil)
	first := startCheck(t, g, t.Context())
	g.processCallback(callback(t, g, f, true))
	awaitDecision(t, first, Approved)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if d, _ := g.Check(ctx, Device{UUID: "550e8400-e29b-41d4-a716-446655440001"}); d != Denied {
		t.Fatal("approval crossed devices")
	}
	g.mu.Lock()
	g.approvedCache[testUUID] = approvedCacheEntry{expiresAt: time.Now().Add(-time.Second)}
	g.mu.Unlock()
	next := startCheck(t, g, t.Context())
	waitFor(t, time.Second, func() bool { g.mu.RLock(); defer g.mu.RUnlock(); return g.pendingRequests[testUUID] != nil })
	select {
	case d := <-next:
		t.Fatalf("expired cache completed Check: %v", d)
	default:
	}
	_ = g.Close()
	awaitDecision(t, next, Denied)
}
