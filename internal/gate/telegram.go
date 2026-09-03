package gate

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/maruina/azath/internal/crypto"
)

// telegramGateConfig holds the configuration for TelegramGate.
type telegramGateConfig struct {
	serverName       string
	botToken         []byte // zeroed on Close
	chatID           string
	authorizedUserID int64
	approvalTTL      time.Duration
	approvalCacheTTL time.Duration
}

// callbackData is the payload embedded in Telegram inline keyboard buttons.
type callbackData struct {
	DeviceUUID string `json:"uuid"`
	Action     string `json:"action"` // "approve" or "deny"
	Timestamp  int64  `json:"ts"`     // Unix timestamp when created
	Token      string `json:"token"`  // HMAC signature to prevent tampering
}

// pendingRequest tracks an unseal request awaiting approval.
type pendingRequest struct {
	device    Device
	createdAt time.Time
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
	pendingRequests map[string]pendingRequest // keyed by canonical UUID
	approvedCache   map[string]approvedCacheEntry

	hmacKey []byte // for signing callback data; zeroed on Close

	httpClient *http.Client

	// shutdown signals the polling goroutine to stop.
	shutdown context.CancelFunc
	wg       sync.WaitGroup

	// lastUpdateID tracks the highest update_id processed to avoid re-processing.
	lastUpdateID int64
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
			authorizedUserID: authorizedUserID,
			approvalTTL:      approvalTTL,
			approvalCacheTTL: approvalCacheTTL,
		},
		pendingRequests: make(map[string]pendingRequest),
		approvedCache:   make(map[string]approvedCacheEntry),
		hmacKey:         hmacKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		shutdown: cancel,
	}

	// Start the long-polling goroutine.
	g.wg.Add(1)
	go g.pollLoop(ctx)

	return g, nil
}

// Check implements Gate.Check.
func (g *TelegramGate) Check(ctx context.Context, device Device) (Decision, error) {
	uuid := device.UUID

	// Check approval cache first (if enabled).
	if g.cfg.approvalCacheTTL > 0 {
		g.mu.RLock()
		entry, cached := g.approvedCache[uuid]
		g.mu.RUnlock()

		if cached && time.Now().Before(entry.expiresAt) {
			return Approved, nil
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// If there's already a pending request for this UUID, deny immediately.
	if _, exists := g.pendingRequests[uuid]; exists {
		return Denied, nil
	}

	// Create pending request.
	g.pendingRequests[uuid] = pendingRequest{
		device:    device,
		createdAt: time.Now(),
	}

	// Send approval message asynchronously so we don't block the Unseal RPC.
	// The approval will be received via the polling loop and matched by UUID.
	go func() {
		if err := g.sendApprovalMessage(device); err != nil {
			// Log would go here if we had a logger passed in.
			// For now, just remove the pending request on failure.
			g.mu.Lock()
			delete(g.pendingRequests, uuid)
			g.mu.Unlock()
		}
	}()

	return Pending, nil
}

// Close shuts down the Telegram gate and zeros sensitive material.
func (g *TelegramGate) Close() error {
	g.shutdown()
	g.wg.Wait()

	crypto.Zero(g.cfg.botToken)
	crypto.Zero(g.hmacKey)

	g.mu.Lock()
	clear(g.pendingRequests)
	clear(g.approvedCache)
	g.mu.Unlock()

	return nil
}

// sendApprovalMessage sends a Telegram message with inline keyboard buttons.
func (g *TelegramGate) sendApprovalMessage(device Device) error {
	now := time.Now().Unix()

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

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", string(g.cfg.botToken))
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending message: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API error %d: %s", resp.StatusCode, string(respBody))
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
			// On error, wait before retrying to avoid tight loops.
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
	url := fmt.Sprintf(
		"https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30",
		string(g.cfg.botToken),
		g.lastUpdateID+1,
	)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching updates: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API error %d: %s", resp.StatusCode, string(respBody))
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

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
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

	// Check chat ID.
	if cb.Message.Chat.ID != 0 {
		// Parse chatID as int64 for comparison.
		var expectedChatID int64
		if _, err := fmt.Sscanf(g.cfg.chatID, "%d", &expectedChatID); err != nil {
			return true
		}
		if expectedChatID != 0 && cb.Message.Chat.ID != expectedChatID {
			return false
		}
	}

	return true
}

// processCallback handles an approved or denied callback.
func (g *TelegramGate) processCallback(data callbackData) {
	g.mu.Lock()
	defer g.mu.Unlock()

	uuid := data.DeviceUUID

	// Remove pending request.
	pending, exists := g.pendingRequests[uuid]
	if !exists {
		// Stale or duplicate callback — ignore.
		return
	}
	delete(g.pendingRequests, uuid)

	if data.Action == "approve" {
		// Add to approval cache if caching is enabled.
		if g.cfg.approvalCacheTTL > 0 {
			g.approvedCache[uuid] = approvedCacheEntry{
				expiresAt: time.Now().Add(g.cfg.approvalCacheTTL),
			}
		}
		// Note: We can't directly signal back to the waiting Check call.
		// The approval is stored in cache for subsequent requests.
		// The original request will timeout or be retried.
		_ = pending // could log device name here
	}
}

// signCallbackData creates an HMAC signature for callback data.
func (g *TelegramGate) signCallbackData(data callbackData) string {
	payload := fmt.Sprintf("%s:%s:%d", data.DeviceUUID, data.Action, data.Timestamp)
	mac := hmac.New(sha256.New, g.hmacKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// encodeCallbackData serializes and signs callback data.
func (g *TelegramGate) encodeCallbackData(data callbackData) string {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		// This should never happen with our simple struct.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(jsonBytes)
}

// decodeCallbackData deserializes and verifies callback data.
func (g *TelegramGate) decodeCallbackData(encoded string) (callbackData, error) {
	var data callbackData

	jsonBytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return data, fmt.Errorf("decoding base64: %w", err)
	}

	if err := json.Unmarshal(jsonBytes, &data); err != nil {
		return data, fmt.Errorf("unmarshaling JSON: %w", err)
	}

	// Verify signature.
	expectedSig := g.signCallbackData(data)
	if !hmac.Equal([]byte(data.Token), []byte(expectedSig)) {
		return data, errors.New("invalid signature")
	}

	// Check timestamp is within approval TTL.
	if time.Since(time.Unix(data.Timestamp, 0)) > g.cfg.approvalTTL {
		return data, errors.New("callback expired")
	}

	return data, nil
}
