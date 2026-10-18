package gate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTelegramResponseLimits(t *testing.T) {
	t.Parallel()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"padding":"` + strings.Repeat("x", 2<<20) + `"}`))
	}))
	defer api.Close()
	g, err := NewTelegramGate("test", []byte(testBotToken), testChatID, testUserID, time.Minute, 0, nil, WithAPIBase(api.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	if g.httpClient.Timeout <= 30*time.Second {
		t.Fatal("HTTP timeout must exceed Telegram long poll")
	}
	awaitDecision(t, startCheck(t, g, t.Context()), Denied)
}
