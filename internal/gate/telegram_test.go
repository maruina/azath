package gate

import (
	"encoding/base64"
	"encoding/json"
	"io"
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

// fakeTelegram is a Telegram Bot API stub. It records sendMessage payloads
// and serves queued getUpdates results.
type fakeTelegram struct {
	mu        sync.Mutex
	server    *httptest.Server
	approve   []string         // callback_data values captured from sendMessage buttons
	deny      []string         // callback_data values captured from sendMessage buttons
	updates   []map[string]any // queued getUpdates result entries
	sendError int              // if nonzero, sendMessage responds with this status
	pollError int              // if nonzero, getUpdates responds with this status
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	ft := &fakeTelegram{}
	ft.server = httptest.NewServer(http.HandlerFunc(ft.handler))
	t.Cleanup(ft.server.Close)
	return ft
}

func (f *fakeTelegram) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		f.handleSendMessage(w, r)
	case strings.HasSuffix(r.URL.Path, "/getUpdates"):
		f.handleGetUpdates(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTelegram) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendError != 0 {
		w.WriteHeader(f.sendError)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var body struct {
		ReplyMarkup struct {
			InlineKeyboard [][]struct {
				CallbackData string `json:"callback_data"`
			} `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.approve = append(f.approve, body.ReplyMarkup.InlineKeyboard[0][0].CallbackData)
	f.deny = append(f.deny, body.ReplyMarkup.InlineKeyboard[0][1].CallbackData)
	writeOK(w)
}

func (f *fakeTelegram) handleGetUpdates(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pollError != 0 {
		w.WriteHeader(f.pollError)
		return
	}
	var result []map[string]any
	if len(f.updates) > 0 {
		result = f.updates
		f.updates = nil
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (f *fakeTelegram) sentApprovals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.approve...)
}

func (f *fakeTelegram) queueUpdate(update map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, update)
}

func (f *fakeTelegram) setSendError(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendError = code
}

func (f *fakeTelegram) setPollError(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pollError = code
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// gateParams holds the constructor inputs a test may vary.
type gateParams struct {
	serverName       string
	chatID           string
	authorizedUserID int64
}

// newTestGate builds a TelegramGate pointed at the given stub.
func newTestGate(t *testing.T, ft *fakeTelegram, approvalTTL, cacheTTL time.Duration, mutate func(*gateParams)) *TelegramGate {
	t.Helper()
	params := gateParams{serverName: "test-server", chatID: testChatID, authorizedUserID: testUserID}
	if mutate != nil {
		mutate(&params)
	}
	g, err := NewTelegramGate(
		params.serverName,
		[]byte(testBotToken),
		params.chatID,
		params.authorizedUserID,
		approvalTTL,
		cacheTTL,
		nil,
		WithAPIBase(ft.server.URL),
	)
	if err != nil {
		t.Fatalf("NewTelegramGate: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// queueCallback queues an update carrying the given callback. Unsigned
// callbacks are signed with the gate's own HMAC key.
func queueCallback(t *testing.T, g *TelegramGate, ft *fakeTelegram, updateID int64, userID, chatID int64, cb callbackData) {
	t.Helper()
	if cb.Token == "" {
		cb.Token = g.signCallbackData(cb)
	}
	payload, err := json.Marshal(cb)
	if err != nil {
		t.Fatalf("marshaling callback: %v", err)
	}
	ft.queueUpdate(map[string]any{
		"update_id": updateID,
		"callback_query": map[string]any{
			"id":   "cb",
			"from": map[string]any{"id": userID},
			"message": map[string]any{
				"chat": map[string]any{"id": chatID},
			},
			"data": base64.RawURLEncoding.EncodeToString(payload),
		},
	})
}

// checkDevice runs one Check call for the test device.
func checkDevice(t *testing.T, g *TelegramGate) (Decision, error) {
	t.Helper()
	return g.Check(t.Context(), Device{Name: testDeviceName, UUID: testUUID})
}

// waitForApprovalMessage waits until the stub has captured a sendMessage call.
func waitForApprovalMessage(t *testing.T, ft *fakeTelegram) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool { return len(ft.sentApprovals()) > 0 })
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func TestNewTelegramGate_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*gateParams)
	}{
		{name: "empty server name", mutate: func(p *gateParams) { p.serverName = "" }},
		{name: "empty chat id", mutate: func(p *gateParams) { p.chatID = "" }},
		{name: "non-numeric chat id", mutate: func(p *gateParams) { p.chatID = "@mychannel" }},
		{name: "zero user id", mutate: func(p *gateParams) { p.authorizedUserID = 0 }},
		{name: "negative user id", mutate: func(p *gateParams) { p.authorizedUserID = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			params := gateParams{serverName: "s", chatID: "111", authorizedUserID: 42}
			tt.mutate(&params)
			_, err := NewTelegramGate(params.serverName, []byte(testBotToken), params.chatID, params.authorizedUserID, time.Minute, 0, nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestNewTelegramGate_RejectsBadTTLs(t *testing.T) {
	t.Parallel()
	if _, err := NewTelegramGate("s", []byte(testBotToken), "111", 42, 0, 0, nil); err == nil {
		t.Error("expected error for zero approval TTL, got nil")
	}
	if _, err := NewTelegramGate("s", []byte(testBotToken), "111", 42, time.Minute, -time.Second, nil); err == nil {
		t.Error("expected error for negative cache TTL, got nil")
	}
}

func TestCheck_PendsAndStoresOnePendingPerUUID(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	g := newTestGate(t, ft, time.Minute, time.Minute, nil)

	decision, err := checkDevice(t, g)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if decision != Pending {
		t.Errorf("first Check decision = %v, want Pending", decision)
	}

	// A second Check while pending must deny immediately.
	decision, err = checkDevice(t, g)
	if err != nil {
		t.Fatalf("second Check: %v", err)
	}
	if decision != Denied {
		t.Errorf("second Check decision = %v, want Denied", decision)
	}

	g.mu.Lock()
	pending := len(g.pendingRequests)
	g.mu.Unlock()
	if pending != 1 {
		t.Errorf("pending requests = %d, want 1", pending)
	}
}

func TestCheck_PendingExpiresAfterTTL(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	g := newTestGate(t, ft, 20*time.Millisecond, time.Minute, nil)

	if _, err := checkDevice(t, g); err != nil {
		t.Fatalf("Check: %v", err)
	}

	// While pending, Check denies without sending a new prompt.
	if d, _ := checkDevice(t, g); d != Denied {
		t.Errorf("decision while pending = %v, want Denied", d)
	}

	// After the TTL, the pending entry must be purged so a fresh prompt is
	// sent instead of denying forever.
	waitFor(t, 2*time.Second, func() bool {
		d, _ := checkDevice(t, g)
		return d == Pending
	})
}

func TestCheck_SendMessageFailure_DeletesPending(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	ft.setSendError(http.StatusInternalServerError)
	g := newTestGate(t, ft, time.Minute, time.Minute, nil)

	// Check returns Pending synchronously; the async send fails and the
	// pending entry must be deleted so the device is not stuck denied.
	if _, err := checkDevice(t, g); err != nil {
		t.Fatalf("Check: %v", err)
	}

	waitFor(t, 5*time.Second, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return len(g.pendingRequests) == 0
	})

	// A later Check can create a new pending request.
	decision, err := checkDevice(t, g)
	if err != nil {
		t.Fatalf("Check after send failure: %v", err)
	}
	if decision != Pending {
		t.Errorf("decision after send failure = %v, want Pending", decision)
	}
}

func TestCallback_Approve_CachesApproval(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	g := newTestGate(t, ft, time.Minute, time.Minute, nil)

	if _, err := checkDevice(t, g); err != nil {
		t.Fatalf("Check: %v", err)
	}
	waitForApprovalMessage(t, ft)

	queueCallback(t, g, ft, 1, testUserID, 111, callbackData{
		DeviceUUID: testUUID,
		Action:     "approve",
		Timestamp:  time.Now().Unix(),
	})

	waitFor(t, 5*time.Second, func() bool {
		d, err := checkDevice(t, g)
		return err == nil && d == Approved
	})
}

func TestCallback_Approve_CacheDisabled(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	g := newTestGate(t, ft, time.Minute, 0, nil)

	if _, err := checkDevice(t, g); err != nil {
		t.Fatalf("Check: %v", err)
	}
	waitForApprovalMessage(t, ft)

	queueCallback(t, g, ft, 1, testUserID, 111, callbackData{
		DeviceUUID: testUUID,
		Action:     "approve",
		Timestamp:  time.Now().Unix(),
	})

	// Pending entry is deleted...
	waitFor(t, 5*time.Second, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return len(g.pendingRequests) == 0
	})

	// ...but with caching disabled the next Check pends again.
	decision, err := checkDevice(t, g)
	if err != nil {
		t.Fatalf("Check after approve: %v", err)
	}
	if decision != Pending {
		t.Errorf("decision after approve with cache disabled = %v, want Pending", decision)
	}
}

func TestCallback_Deny_DeletesPending(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	g := newTestGate(t, ft, time.Minute, time.Minute, nil)

	if _, err := checkDevice(t, g); err != nil {
		t.Fatalf("Check: %v", err)
	}
	waitForApprovalMessage(t, ft)

	queueCallback(t, g, ft, 1, testUserID, 111, callbackData{
		DeviceUUID: testUUID,
		Action:     "deny",
		Timestamp:  time.Now().Unix(),
	})

	// Wait for the poll loop to process the deny.
	waitFor(t, 5*time.Second, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return len(g.pendingRequests) == 0
	})

	// After a deny, a new Check must send a fresh prompt (Pending), not stay
	// stuck on the deleted pending entry.
	decision, err := checkDevice(t, g)
	if err != nil {
		t.Fatalf("Check after deny: %v", err)
	}
	if decision != Pending {
		t.Errorf("decision after deny = %v, want Pending", decision)
	}
}

func TestCallback_Rejections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		userID int64
		chatID int64
		mutate func(*callbackData)
	}{
		{
			name: "wrong user", userID: 999, chatID: 111,
		},
		{
			name: "wrong chat", userID: testUserID, chatID: 999,
		},
		{
			// A callback without a message chat must fail closed.
			name: "missing chat fails closed", userID: testUserID, chatID: 0,
		},
		{
			name: "expired callback", userID: testUserID, chatID: 111,
			mutate: func(cb *callbackData) { cb.Timestamp = time.Now().Add(-2 * time.Minute).Unix() },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ft := newFakeTelegram(t)
			g := newTestGate(t, ft, time.Minute, time.Minute, nil)

			if _, err := checkDevice(t, g); err != nil {
				t.Fatalf("Check: %v", err)
			}
			waitForApprovalMessage(t, ft)

			cb := callbackData{DeviceUUID: testUUID, Action: "approve", Timestamp: time.Now().Unix()}
			if tt.mutate != nil {
				tt.mutate(&cb)
			}
			queueCallback(t, g, ft, 1, tt.userID, tt.chatID, cb)

			// Give the poll loop a chance to process the hostile callback,
			// then require that nothing was approved and pending remains.
			time.Sleep(2 * time.Second)
			g.mu.Lock()
			_, pending := g.pendingRequests[testUUID]
			_, cached := g.approvedCache[testUUID]
			g.mu.Unlock()
			if !pending {
				t.Error("pending entry was deleted by rejected callback")
			}
			if cached {
				t.Error("rejected callback added an approval cache entry")
			}
		})
	}
}

func TestCallback_TamperedSignature_NotApproved(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	g := newTestGate(t, ft, time.Minute, time.Minute, nil)

	if _, err := checkDevice(t, g); err != nil {
		t.Fatalf("Check: %v", err)
	}
	waitForApprovalMessage(t, ft)

	// Sign a valid callback, then change the UUID so the HMAC check fails
	// while the payload still decodes.
	cb := callbackData{DeviceUUID: testUUID, Action: "approve", Timestamp: time.Now().Unix()}
	cb.Token = g.signCallbackData(cb)
	cb.DeviceUUID = "550e8400-e29b-41d4-a716-446655440001"

	queueCallback(t, g, ft, 1, testUserID, 111, cb)

	time.Sleep(2 * time.Second)
	g.mu.Lock()
	_, cached := g.approvedCache[testUUID]
	g.mu.Unlock()
	if cached {
		t.Error("tampered callback added an approval cache entry")
	}
}

func TestCallback_StaleUUID_Ignored(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	g := newTestGate(t, ft, time.Minute, time.Minute, nil)

	// Approve for a UUID with no pending request: must not create cache state.
	queueCallback(t, g, ft, 1, testUserID, 111, callbackData{
		DeviceUUID: testUUID,
		Action:     "approve",
		Timestamp:  time.Now().Unix(),
	})

	time.Sleep(2 * time.Second)
	g.mu.Lock()
	_, cached := g.approvedCache[testUUID]
	g.mu.Unlock()
	if cached {
		t.Error("stale callback added an approval cache entry")
	}
}

func TestGetUpdates_APIFailure_FailsClosed(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	ft.setPollError(http.StatusInternalServerError)
	g := newTestGate(t, ft, time.Minute, time.Minute, nil)

	// The poll loop must survive API errors, and Check must still pend
	// (fail closed happens server-side via the gate error contract).
	decision, err := checkDevice(t, g)
	if err != nil {
		t.Fatalf("Check with failing Telegram API: %v", err)
	}
	if decision != Pending {
		t.Errorf("decision = %v, want Pending", decision)
	}
}

func TestClose_ZerosTokenAndHMACKey(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	g := newTestGate(t, ft, time.Minute, time.Minute, nil)

	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for i, b := range g.cfg.botToken {
		if b != 0 {
			t.Errorf("botToken[%d] not zeroed", i)
		}
	}
	for i, b := range g.hmacKey {
		if b != 0 {
			t.Errorf("hmacKey[%d] not zeroed", i)
		}
	}

	// Close is idempotent.
	if err := g.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
