package gate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testBotToken         = "123456:TEST-TOKEN"
	testChatID           = "111"
	testUserID     int64 = 42
	testUUID             = "550e8400-e29b-41d4-a716-446655440000"
	testDeviceName       = "synology"
)

type fakeTelegram struct {
	mu        sync.Mutex
	server    *httptest.Server
	approve   []string
	deny      []string
	updates   []map[string]any
	sendError int
	pollError int
	sends     int
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			f.sends++
			if f.sendError != 0 {
				w.WriteHeader(f.sendError)
				return
			}
			var body struct {
				ReplyMarkup struct {
					InlineKeyboard [][]struct {
						CallbackData string `json:"callback_data"`
					} `json:"inline_keyboard"`
				} `json:"reply_markup"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(400)
				return
			}
			for _, row := range body.ReplyMarkup.InlineKeyboard {
				for _, button := range row {
					if len(button.CallbackData) == 0 || len(button.CallbackData) > 64 {
						w.WriteHeader(400)
						return
					}
				}
			}
			f.approve = append(f.approve, body.ReplyMarkup.InlineKeyboard[0][0].CallbackData)
			f.deny = append(f.deny, body.ReplyMarkup.InlineKeyboard[0][1].CallbackData)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		} else {
			if f.pollError != 0 {
				w.WriteHeader(f.pollError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": f.updates})
			f.updates = nil
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *fakeTelegram) sentApprovals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.approve...)
}
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}
func waitForApprovalMessage(t *testing.T, f *fakeTelegram) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool { return len(f.sentApprovals()) > 0 })
}
func newTestGate(t *testing.T, f *fakeTelegram, ttl, cache time.Duration, _ any) *TelegramGate {
	t.Helper()
	g, err := NewTelegramGate("test", []byte(testBotToken), testChatID, testUserID, ttl, cache, nil, WithAPIBase(f.server.URL), WithPromptRateLimit(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}
func checkDevice(t *testing.T, g *TelegramGate) (Decision, error) {
	t.Helper()
	return g.Check(t.Context(), Device{Name: testDeviceName, UUID: testUUID})
}
func callback(t *testing.T, g *TelegramGate, f *fakeTelegram, approve bool) callbackData {
	t.Helper()
	waitForApprovalMessage(t, f)
	f.mu.Lock()
	encoded := f.approve[len(f.approve)-1]
	if !approve {
		encoded = f.deny[len(f.deny)-1]
	}
	f.mu.Unlock()
	cb, err := g.decodeCallbackData(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return cb
}

func TestNewTelegramGateValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, server, token, chat string
		user                      int64
		ttl, cache                time.Duration
	}{
		{"server", "", "t", "111", 42, time.Minute, 0}, {"token", "s", "", "111", 42, time.Minute, 0},
		{"chat", "s", "t", "bad", 42, time.Minute, 0}, {"user", "s", "t", "111", 0, time.Minute, 0},
		{"ttl", "s", "t", "111", 42, 0, 0}, {"cache", "s", "t", "111", 42, time.Minute, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if g, err := NewTelegramGate(tt.server, []byte(tt.token), tt.chat, tt.user, tt.ttl, tt.cache, nil); err == nil {
				_ = g.Close()
				t.Fatal("accepted invalid config")
			}
		})
	}
}

func TestBlockingDeduplicationAndCache(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	g := newTestGate(t, f, time.Minute, time.Minute, nil)
	first := startCheck(t, g, t.Context())
	waitForApprovalMessage(t, f)
	second := startCheck(t, g, t.Context())
	waitFor(t, time.Second, func() bool { g.mu.RLock(); defer g.mu.RUnlock(); return g.pendingRequests[testUUID].waiters == 2 })
	g.processCallback(callback(t, g, f, true))
	awaitDecision(t, first, Approved)
	awaitDecision(t, second, Approved)
	if d, _ := checkDevice(t, g); d != Approved {
		t.Fatal("cache miss")
	}
	if len(f.sentApprovals()) != 1 {
		t.Fatal("duplicate prompt")
	}
	other, cancel := context.WithCancel(t.Context())
	cancel()
	if d, _ := g.Check(other, Device{UUID: "550e8400-e29b-41d4-a716-446655440001"}); d != Denied {
		t.Fatal("other UUID approved")
	}
}

func TestBlockingCancellation(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	g := newTestGate(t, f, time.Minute, 0, nil)
	ctx, cancel := context.WithCancel(t.Context())
	first := startCheck(t, g, ctx)
	waitForApprovalMessage(t, f)
	second := startCheck(t, g, t.Context())
	waitFor(t, time.Second, func() bool { g.mu.RLock(); defer g.mu.RUnlock(); return g.pendingRequests[testUUID].waiters == 2 })
	cancel()
	awaitDecision(t, first, Denied)
	g.processCallback(callback(t, g, f, true))
	awaitDecision(t, second, Approved)
	ctx, cancel = context.WithCancel(t.Context())
	result := startCheck(t, g, ctx)
	waitFor(t, time.Second, func() bool { g.mu.RLock(); defer g.mu.RUnlock(); return len(g.pendingRequests) == 1 })
	cancel()
	awaitDecision(t, result, Denied)
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.pendingRequests) != 0 {
		t.Fatal("pending request leaked")
	}
}

func TestLateApprovalAfterClientCancellationCachesApproval(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	g := newTestGate(t, f, time.Minute, time.Minute, nil)

	ctx, cancel := context.WithCancel(t.Context())
	check := startCheck(t, g, ctx)
	waitForApprovalMessage(t, f)

	// Client aborts/cancels early before operator responds
	cancel()
	awaitDecision(t, check, Denied)

	// Operator later taps Approve on Telegram within approval TTL
	cb := callback(t, g, f, true)
	g.processCallback(cb)

	// Client reconnects/retries: should succeed via cached approval
	retryCtx := t.Context()
	decision, err := g.Check(retryCtx, Device{Name: "synology", UUID: testUUID})
	if err != nil {
		t.Fatalf("unexpected error on retry: %v", err)
	}
	if decision != Approved {
		t.Fatalf("expected retry to be Approved via cache, got %v", decision)
	}
}

func TestBlockingTimeoutAndRateLimit(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"timeout", "deny", "api"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			f := newFakeTelegram(t)
			if outcome == "api" {
				f.sendError = 500
			}
			g := newTestGate(t, f, 100*time.Millisecond, 0, nil)
			g.mu.Lock()
			g.promptInterval = time.Hour
			g.mu.Unlock()
			result := startCheck(t, g, t.Context())
			if outcome == "deny" {
				g.processCallback(callback(t, g, f, false))
			}
			awaitDecision(t, result, Denied)
			if d, _ := checkDevice(t, g); d != Denied {
				t.Fatal("rate limit did not deny")
			}
			f.mu.Lock()
			sends := f.sends
			f.mu.Unlock()
			if sends != 1 {
				t.Fatalf("sends = %d", sends)
			}
			g.mu.Lock()
			g.lastPrompt[testUUID] = time.Now().Add(-2 * time.Hour)
			g.mu.Unlock()
			next := startCheck(t, g, t.Context())
			awaitDecision(t, next, Denied)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.sends != 2 {
				t.Fatal("rate limit did not expire")
			}
		})
	}
}

func TestBlockingWaiterLimit(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	g := newTestGate(t, f, time.Minute, 0, nil)
	results := make([]<-chan Decision, maxWaitersPerDevice)
	for i := range results {
		results[i] = startCheck(t, g, t.Context())
	}
	waitFor(t, time.Second, func() bool {
		g.mu.RLock()
		defer g.mu.RUnlock()
		p := g.pendingRequests[testUUID]
		return p != nil && p.waiters == maxWaitersPerDevice
	})
	if d, _ := checkDevice(t, g); d != Denied {
		t.Fatal("waiter limit not enforced")
	}
	_ = g.Close()
	for _, r := range results {
		awaitDecision(t, r, Denied)
	}
	for _, b := range append(g.cfg.botToken, g.hmacKey...) {
		if b != 0 {
			t.Fatal("secret not cleared")
		}
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCallbackRejections(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"user", "chat", "missing chat", "expired", "tampered", "malformed", "stale"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newFakeTelegram(t)
			g := newTestGate(t, f, time.Minute, 0, nil)
			result := startCheck(t, g, t.Context())
			cb := callback(t, g, f, true)
			user, chat := testUserID, int64(111)
			switch kind {
			case "user":
				user = 999
			case "chat":
				chat = 999
			case "missing chat":
				chat = 0
			case "expired":
				cb.Timestamp = time.Now().Add(-time.Hour).UnixNano()
				cb.Token = g.signCallbackData(cb)
			case "tampered":
				cb.Token = strings.Repeat("A", 22)
			case "stale":
				cb.Timestamp--
				cb.Token = g.signCallbackData(cb)
			}
			encoded := g.encodeCallbackData(cb)
			if kind == "malformed" {
				encoded = "bad"
			}
			f.mu.Lock()
			f.updates = append(f.updates, map[string]any{"update_id": 1, "callback_query": map[string]any{"id": "cb", "from": map[string]any{"id": user}, "message": map[string]any{"chat": map[string]any{"id": chat}}, "data": encoded}})
			f.mu.Unlock()
			waitFor(t, 3*time.Second, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.updates) == 0 })
			select {
			case d := <-result:
				t.Fatalf("hostile callback completed request: %v", d)
			case <-time.After(20 * time.Millisecond):
			}
			_ = g.Close()
			awaitDecision(t, result, Denied)
		})
	}
}

func TestZeroGateDenied(t *testing.T) {
	t.Parallel()
	g := new(TelegramGate)
	if d, _ := checkDevice(t, g); d != Denied {
		t.Fatal("zero gate approved")
	}
	_ = g.Close()
}
