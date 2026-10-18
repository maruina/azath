package gate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendLogicalAPIFailure(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"ok":false}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/sendMessage") {
					_, _ = w.Write([]byte(body))
					return
				}
				_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
			}))
			defer api.Close()
			g, err := NewTelegramGate("test", []byte(testBotToken), testChatID, testUserID, time.Minute, 0, nil, WithAPIBase(api.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = g.Close() }()
			awaitDecision(t, startCheck(t, g, t.Context()), Denied)
			if d, _ := checkDevice(t, g); d != Denied {
				t.Fatal("API failure bypassed rate limit")
			}
		})
	}
}

func TestPollAPIFailureFailsClosed(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	f.pollError = 500
	g := newTestGate(t, f, 30*time.Millisecond, 0, nil)
	awaitDecision(t, startCheck(t, g, t.Context()), Denied)
}

func TestCanceledCheckDoesNotPrompt(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	g := newTestGate(t, f, time.Minute, time.Minute, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d, err := g.Check(ctx, Device{UUID: testUUID})
	if d != Denied || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Check: %v, %v", d, err)
	}
	if len(f.sentApprovals()) != 0 {
		t.Fatal("canceled Check prompted")
	}
}
