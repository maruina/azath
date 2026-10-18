package gate

import (
	"context"
	"testing"
	"time"
)

func startCheck(t *testing.T, g *TelegramGate, ctx context.Context) <-chan Decision {
	t.Helper()
	result := make(chan Decision, 1)
	go func() { d, _ := g.Check(ctx, Device{Name: testDeviceName, UUID: testUUID}); result <- d }()
	return result
}

func awaitDecision(t *testing.T, result <-chan Decision, want Decision) {
	t.Helper()
	select {
	case got := <-result:
		if got != want {
			t.Fatalf("decision = %v, want %v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Check did not complete")
	}
}

func TestBlockingApprovalWithoutCache(t *testing.T) {
	t.Parallel()
	ft := newFakeTelegram(t)
	g := newTestGate(t, ft, time.Minute, 0, nil)
	result := startCheck(t, g, t.Context())
	waitForApprovalMessage(t, ft)
	select {
	case d := <-result:
		t.Fatalf("Check returned before approval: %v", d)
	default:
	}
	cb, err := g.decodeCallbackData(ft.sentApprovals()[0])
	if err != nil {
		t.Fatal(err)
	}
	g.processCallback(cb)
	awaitDecision(t, result, Approved)
	next := startCheck(t, g, t.Context())
	waitFor(t, time.Second, func() bool { return len(ft.sentApprovals()) == 2 })
	select {
	case d := <-next:
		t.Fatalf("later Check completed without fresh approval: %v", d)
	default:
	}
	g.processCallback(callback(t, g, ft, true))
	awaitDecision(t, next, Approved)
}
