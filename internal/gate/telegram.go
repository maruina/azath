package gate

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/maruina/azath/internal/crypto"
)

// telegramGateConfig holds the configuration for TelegramGate.
type telegramGateConfig struct {
	serverName       string
	botToken         []byte // zeroed on Close
	chatID           string
	chatIDInt        int64 // parsed chatID; validated at construction
	authorizedUserID int64
	approvalTTL      time.Duration
	approvalCacheTTL time.Duration
}

// telegramAPIBase is the Telegram Bot API base URL.
const telegramAPIBase = "https://api.telegram.org"

// Option configures a TelegramGate.
type Option func(*TelegramGate)

// WithAPIBase overrides the Telegram Bot API base URL. Tests use it to point
// the gate at a local stub; production callers never set it.
func WithAPIBase(base string) Option {
	return func(g *TelegramGate) { g.apiBase = base }
}

// callbackData is the payload embedded in Telegram inline keyboard buttons.
type callbackData struct {
	DeviceUUID string `json:"uuid"`
	Action     string `json:"action"` // "approve" or "deny"
	Timestamp  int64  `json:"ts"`     // Unix nanoseconds; identifies one pending request
	Token      string `json:"token"`  // HMAC signature to prevent tampering
}

// pendingRequest tracks an unseal request awaiting approval.
type pendingRequest struct {
	device    Device
	createdAt time.Time
	done      chan struct{}
	decision  Decision
	waiters   int
	cancel    context.CancelFunc
}

// deliberate: fixed homelab limits; make these configurable if larger fleets need them.
const (
	maxPendingRequests  = 64
	maxWaitersPerDevice = 32
	maxConcurrentSends  = 8
	maxTrackedDevices   = 1024
)

// WithPromptRateLimit sets the minimum interval between prompts for one device.
// Zero disables the interval; concurrency and retained-state limits still apply.
func WithPromptRateLimit(interval time.Duration) Option {
	return func(g *TelegramGate) { g.promptInterval = interval }
}

// approvedCacheEntry tracks an approved device with its expiry time.
type approvedCacheEntry struct {
	expiresAt time.Time
}

// TelegramGate implements Gate using Telegram bot for approval.
// It uses long polling (getUpdates) rather than webhooks.
type TelegramGate struct {
	cfg telegramGateConfig

	mu              sync.RWMutex
	pendingRequests map[string]*pendingRequest // keyed by canonical UUID
	lastPrompt      map[string]time.Time
	promptInterval  time.Duration
	sends           chan struct{}
	approvedCache   map[string]approvedCacheEntry

	hmacKey []byte // for signing callback data; zeroed on Close

	httpClient *http.Client

	// shutdown signals the polling goroutine to stop.
	shutdown  context.CancelFunc
	sendCtx   context.Context
	closed    bool
	closeOnce sync.Once
	wg        sync.WaitGroup

	// lastUpdateID tracks the highest update_id processed to avoid re-processing.
	lastUpdateID int64

	logger *slog.Logger

	// apiBase is the Telegram Bot API base URL. Overridden in tests to point
	// at an httptest server.
	apiBase string
}

// NewTelegramGate creates a new TelegramGate.
// The bot token is copied; caller may zero their copy immediately.
func NewTelegramGate(
	serverName string,
	botToken []byte,
	chatID string,
	authorizedUserID int64,
	approvalTTL time.Duration,
	approvalCacheTTL time.Duration,
	logger *slog.Logger,
	opts ...Option,
) (*TelegramGate, error) {
	if serverName == "" {
		return nil, errors.New("server name is required")
	}
	if len(botToken) == 0 {
		return nil, errors.New("bot token is required")
	}
	if chatID == "" {
		return nil, errors.New("chat ID is required")
	}
	// Parse the chat ID once here so callback validation can fail closed
	// instead of skipping the chat check on a parse error.
	chatIDInt, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("chat ID must be a numeric Telegram chat ID: %w", err)
	}
	if authorizedUserID <= 0 {
		return nil, errors.New("authorized user ID must be positive")
	}
	if approvalTTL <= 0 {
		return nil, errors.New("approval TTL must be positive")
	}
	// approvalCacheTTL of 0 disables caching (valid per spec).
	if approvalCacheTTL < 0 {
		return nil, errors.New("approval cache TTL must not be negative")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	// Generate HMAC key for signing callback data.
	hmacKey := make([]byte, 32)
	if _, err := rand.Read(hmacKey); err != nil {
		return nil, fmt.Errorf("generating HMAC key: %w", err)
	}

	tokenCopy := make([]byte, len(botToken))
	copy(tokenCopy, botToken)

	ctx, cancel := context.WithCancel(context.Background())

	g := &TelegramGate{
		cfg: telegramGateConfig{
			serverName:       serverName,
			botToken:         tokenCopy,
			chatID:           chatID,
			chatIDInt:        chatIDInt,
			authorizedUserID: authorizedUserID,
			approvalTTL:      approvalTTL,
			approvalCacheTTL: approvalCacheTTL,
		},
		pendingRequests: make(map[string]*pendingRequest),
		lastPrompt:      make(map[string]time.Time),
		promptInterval:  5 * time.Minute,
		sends:           make(chan struct{}, maxConcurrentSends),
		approvedCache:   make(map[string]approvedCacheEntry),
		hmacKey:         hmacKey,
		httpClient: &http.Client{
			Timeout: 40 * time.Second,
		},
		shutdown: cancel,
		sendCtx:  ctx,
		logger:   logger,
		apiBase:  telegramAPIBase,
	}

	// Apply options before the polling goroutine starts so the first
	// getUpdates call already uses the configured API base.
	for _, opt := range opts {
		opt(g)
	}

	// Start the long-polling goroutine.
	g.wg.Add(1)
	go g.pollLoop(ctx)

	return g, nil
}

// Check waits for device approval, cancellation, or the approval deadline.
// Concurrent requests for one device share a prompt, but cancel independently.
func (g *TelegramGate) Check(ctx context.Context, device Device) (Decision, error) {
	id, err := uuid.Parse(device.UUID)
	if err != nil {
		return Denied, nil
	}
	if err := ctx.Err(); err != nil {
		return Denied, fmt.Errorf("waiting for approval: %w", err)
	}
	device.UUID = id.String()
	return g.waitForApproval(ctx, device)
}

func (g *TelegramGate) waitForApproval(ctx context.Context, device Device) (Decision, error) {
	uuid := device.UUID

	g.mu.Lock()
	if g.closed || g.sendCtx == nil {
		g.mu.Unlock()
		return Denied, nil
	}
	now := time.Now()
	for id, entry := range g.approvedCache {
		if !now.Before(entry.expiresAt) {
			delete(g.approvedCache, id)
		}
	}
	for id, sent := range g.lastPrompt {
		if now.Sub(sent) >= g.promptInterval {
			delete(g.lastPrompt, id)
		}
	}
	if _, ok := g.approvedCache[uuid]; ok && ctx.Err() == nil {
		g.mu.Unlock()
		return Approved, nil
	}
	pending := g.pendingRequests[uuid]
	if pending == nil {
		if _, limited := g.lastPrompt[uuid]; limited || len(g.pendingRequests) >= maxPendingRequests || len(g.lastPrompt) >= maxTrackedDevices || len(g.approvedCache) >= maxTrackedDevices {
			g.mu.Unlock()
			return Denied, nil
		}
		select {
		case g.sends <- struct{}{}:
		default:
			g.mu.Unlock()
			return Denied, nil
		}
		sendCtx, cancel := context.WithTimeout(g.sendCtx, g.cfg.approvalTTL)
		pending = &pendingRequest{device: device, createdAt: now, done: make(chan struct{}), cancel: cancel}
		g.pendingRequests[uuid] = pending
		g.lastPrompt[uuid] = now
		g.wg.Add(1)
		go g.runPending(sendCtx, pending)
	}
	if pending.waiters >= maxWaitersPerDevice {
		g.mu.Unlock()
		return Denied, nil
	}
	pending.waiters++
	g.wg.Add(1)
	g.mu.Unlock()
	defer g.wg.Done()
	select {
	case <-ctx.Done():
	case <-pending.done:
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	pending.waiters--
	if pending.waiters == 0 {
		g.finishPendingLocked(pending, Denied)
		if ctx.Err() != nil {
			delete(g.lastPrompt, uuid)
		}
	}
	if err := ctx.Err(); err != nil {
		return Denied, fmt.Errorf("waiting for approval: %w", err)
	}
	if g.closed {
		return Denied, nil
	}
	return pending.decision, nil
}

// Close shuts down the Telegram gate and zeros sensitive material.
func (g *TelegramGate) Close() error {
	g.closeOnce.Do(func() {
		g.mu.Lock()
		g.closed = true
		if g.shutdown != nil {
			g.shutdown()
		}
		for _, pending := range g.pendingRequests {
			g.finishPendingLocked(pending, Denied)
		}
		g.mu.Unlock()
		g.wg.Wait()
		crypto.Zero(g.cfg.botToken)
		crypto.Zero(g.hmacKey)
		g.mu.Lock()
		clear(g.pendingRequests)
		clear(g.approvedCache)
		clear(g.lastPrompt)
		g.mu.Unlock()
	})

	return nil
}

func (g *TelegramGate) finishPendingLocked(pending *pendingRequest, decision Decision) {
	if g.pendingRequests[pending.device.UUID] != pending {
		return
	}
	delete(g.pendingRequests, pending.device.UUID)
	pending.decision = decision
	pending.cancel()
	close(pending.done)
}

func (g *TelegramGate) runPending(ctx context.Context, pending *pendingRequest) {
	defer g.wg.Done()
	err := g.sendApprovalMessage(ctx, pending.device, pending.createdAt)
	<-g.sends
	if err != nil {
		g.logger.ErrorContext(ctx, "gate: sending approval message failed", "device_uuid", pending.device.UUID, "error", err)
	} else {
		<-ctx.Done()
	}
	g.mu.Lock()
	g.finishPendingLocked(pending, Denied)
	g.mu.Unlock()
}

// sendApprovalMessage sends a Telegram message with inline keyboard buttons.
func (g *TelegramGate) sendApprovalMessage(ctx context.Context, device Device, createdAt time.Time) error {
	now := createdAt.UnixNano()

	// Build callback data for approve button.
	approveData := callbackData{
		DeviceUUID: device.UUID,
		Action:     "approve",
		Timestamp:  now,
	}
	approveData.Token = g.signCallbackData(approveData)

	// Build callback data for deny button.
	denyData := callbackData{
		DeviceUUID: device.UUID,
		Action:     "deny",
		Timestamp:  now,
	}
	denyData.Token = g.signCallbackData(denyData)

	// Build message text.
	text := fmt.Sprintf(
		"🔐 Azath Unseal Request\n\n"+
			"Server: %s\n"+
			"Device: %s\n"+
			"UUID: %s...",
		g.cfg.serverName,
		device.Name,
		device.UUID[:8],
	)

	// Build inline keyboard JSON.
	keyboard := map[string]interface{}{
		"inline_keyboard": [][]map[string]string{
			{
				{
					"text":          "✅ Approve",
					"callback_data": g.encodeCallbackData(approveData),
				},
				{
					"text":          "❌ Deny",
					"callback_data": g.encodeCallbackData(denyData),
				},
			},
		},
	}

	params := map[string]interface{}{
		"chat_id":      g.cfg.chatID,
		"text":         text,
		"reply_markup": keyboard,
	}

	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshaling params: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", g.apiBase, string(g.cfg.botToken))
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return errors.New("creating sendMessage request failed")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return telegramTransportError("sending message", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API error %d", resp.StatusCode)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return fmt.Errorf("decoding sendMessage response: %w", err)
	}
	if !result.OK {
		return errors.New("telegram API returned ok=false")
	}
	return nil
}

// pollLoop runs the Telegram getUpdates long-polling loop.
func (g *TelegramGate) pollLoop(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := g.getUpdates(ctx); err != nil {
			// On error, wait before retrying to avoid tight loops. Log so that
			// an unreachable Telegram API is observable; Check still fails closed.
			g.logger.ErrorContext(ctx, "gate: getUpdates failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		// Small delay between polls to avoid hammering the API.
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// getUpdates fetches updates from Telegram and processes callbacks.
func (g *TelegramGate) getUpdates(ctx context.Context) error {
	// Build getUpdates URL with offset and timeout.
	url := fmt.Sprintf("%s/bot%s/getUpdates?offset=%d&timeout=30", g.apiBase, string(g.cfg.botToken), g.lastUpdateID+1)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return errors.New("creating getUpdates request failed")
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return telegramTransportError("fetching updates", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API error %d", resp.StatusCode)
	}
	var result struct {
		OK     bool `json:"ok"`
		Result []struct {
			UpdateID      int64 `json:"update_id"`
			CallbackQuery *struct {
				ID   string `json:"id"`
				From struct {
					ID int64 `json:"id"`
				} `json:"from"`
				Message struct {
					Chat struct {
						ID int64 `json:"id"`
					} `json:"chat"`
				} `json:"message"`
				Data string `json:"data"`
			} `json:"callback_query"`
		} `json:"result"`
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if !result.OK {
		return errors.New("telegram API returned ok=false")
	}

	for _, update := range result.Result {
		if update.CallbackQuery == nil {
			continue
		}

		cb := update.CallbackQuery

		// Update lastUpdateID to avoid re-processing.
		if update.UpdateID > g.lastUpdateID {
			g.lastUpdateID = update.UpdateID
		}

		// Validate callback.
		if !g.validateCallback(cb) {
			continue
		}

		// Decode and verify callback data.
		data, err := g.decodeCallbackData(cb.Data)
		if err != nil {
			continue
		}

		// Process the callback.
		g.processCallback(data)
	}

	return nil
}

// validateCallback checks that the callback is from the authorized user and chat.
func (g *TelegramGate) validateCallback(cb *struct {
	ID   string `json:"id"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Message struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
	Data string `json:"data"`
}) bool {
	// Check user ID.
	if cb.From.ID != g.cfg.authorizedUserID {
		return false
	}

	// Check chat ID. Callbacks carry the chat of the message the button was
	// attached to; a missing chat fails closed (ID 0 never matches a valid
	// configured chat).
	if cb.Message.Chat.ID != g.cfg.chatIDInt {
		return false
	}

	return true
}

// processCallback handles an approved or denied callback.
func (g *TelegramGate) processCallback(data callbackData) {
	g.mu.Lock()
	defer g.mu.Unlock()

	uuid := data.DeviceUUID
	created := time.Unix(0, data.Timestamp)
	if created.After(time.Now()) || time.Since(created) > g.cfg.approvalTTL {
		return
	}
	if g.closed {
		return
	}
	if data.Action != "approve" && data.Action != "deny" {
		return
	}

	pending, exists := g.pendingRequests[uuid]
	if exists {
		if pending.createdAt.UnixNano() != data.Timestamp {
			// Callback does not match current pending request.
			return
		}
		decision := Denied
		if data.Action == "approve" {
			decision = Approved
		}
		g.finishPendingLocked(pending, decision)
	}

	if data.Action == "approve" {
		// Add to approval cache if caching is enabled.
		if g.cfg.approvalCacheTTL > 0 && len(g.approvedCache) < maxTrackedDevices {
			g.approvedCache[uuid] = approvedCacheEntry{
				expiresAt: time.Now().Add(g.cfg.approvalCacheTTL),
			}
		}
		deviceName := uuid[:8]
		if exists {
			deviceName = pending.device.Name
		}
		g.logger.InfoContext(context.Background(), "gate: unseal approved",
			"device_uuid", uuid, "device_name", deviceName)
		return
	}

	deviceName := uuid[:8]
	if exists {
		deviceName = pending.device.Name
	}
	g.logger.InfoContext(context.Background(), "gate: unseal denied",
		"device_uuid", uuid, "device_name", deviceName)
}

// signCallbackData creates an HMAC signature for callback data.
func (g *TelegramGate) signCallbackData(data callbackData) string {
	payload := fmt.Sprintf("%s:%s:%d", data.DeviceUUID, data.Action, data.Timestamp)
	mac := hmac.New(sha256.New, g.hmacKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// encodeCallbackData uses 41 bytes (55 base64 characters), below Telegram's 64-byte limit.
func (g *TelegramGate) encodeCallbackData(data callbackData) string {
	id, err := uuid.Parse(data.DeviceUUID)
	if err != nil {
		return ""
	}
	raw := make([]byte, 41)
	copy(raw, id[:])
	_, _ = binary.Encode(raw[16:24], binary.BigEndian, data.Timestamp)
	switch data.Action {
	case "approve":
		raw[24] = 1
	case "deny":
		raw[24] = 2
	default:
		return ""
	}
	signature, err := base64.RawURLEncoding.DecodeString(data.Token)
	if err != nil || len(signature) != 16 {
		return ""
	}
	copy(raw[25:], signature)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (g *TelegramGate) decodeCallbackData(encoded string) (callbackData, error) {
	var data callbackData
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 41 {
		return data, errors.New("invalid callback")
	}
	id, _ := uuid.FromBytes(raw[:16])
	data.DeviceUUID = id.String()
	_, _ = binary.Decode(raw[16:24], binary.BigEndian, &data.Timestamp)
	switch raw[24] {
	case 1:
		data.Action = "approve"
	case 2:
		data.Action = "deny"
	default:
		return data, errors.New("invalid action")
	}
	data.Token = base64.RawURLEncoding.EncodeToString(raw[25:])
	if !hmac.Equal([]byte(data.Token), []byte(g.signCallbackData(data))) {
		return data, errors.New("invalid signature")
	}
	created := time.Unix(0, data.Timestamp)
	if created.After(time.Now()) || time.Since(created) > g.cfg.approvalTTL {
		return data, errors.New("callback expired")
	}
	return data, nil
}

func telegramTransportError(operation string, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	// deliberate: omit arbitrary transport text because it can contain credential-bearing URLs.
	// Add allowlisted error categories if finer diagnostics are needed.
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: transport failure", operation)
}
