package server_test

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	kms "github.com/siderolabs/kms-client/api/kms"

	"github.com/maruina/azath/internal/gate"
	"github.com/maruina/azath/internal/server"
)

type countingGate struct{ calls atomic.Int32 }

func (g *countingGate) Check(context.Context, gate.Device) (gate.Decision, error) {
	g.calls.Add(1)
	return gate.Approved, nil
}
func (*countingGate) Close() error { return nil }

func TestUnsealRejectsInvalidBlobBeforeGate(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := newLoadedManager(t, testKey(), m)
	id := uuid.NewString()
	g := &countingGate{}
	_, client := newTestServerWithGRPC(t, mgr, m,
		server.WithDevices(map[string]server.DeviceInfo{id: {Name: "test", UUID: id}}), server.WithGate(g))
	plaintext := bytes.Repeat([]byte{0x71}, 47)
	blob := sealOne(t, t.Context(), client, id, plaintext)
	for _, data := range [][]byte{append([]byte(nil), blob[:8]...), append([]byte(nil), blob...)} {
		data[len(data)-1] ^= 1
		response, err := client.Unseal(t.Context(), &kms.Request{NodeUuid: id, Data: data})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(response.Data, plaintext) {
			t.Error("invalid blob returned plaintext")
		}
	}
	if g.calls.Load() != 0 {
		t.Error("invalid blob prompted gate")
	}
	response, err := client.Unseal(t.Context(), &kms.Request{NodeUuid: id, Data: blob})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response.Data, plaintext) {
		t.Error("valid wire response changed")
	}
	if g.calls.Load() != 1 {
		t.Error("valid blob did not check gate exactly once")
	}
}

func TestUnsealNilGateDenies(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := newLoadedManager(t, testKey(), m)
	id := uuid.NewString()
	_, client := newTestServerWithGRPC(t, mgr, m,
		server.WithDevices(map[string]server.DeviceInfo{id: {Name: "test", UUID: id}}))
	plaintext := bytes.Repeat([]byte{0x71}, 47)
	blob := sealOne(t, t.Context(), client, id, plaintext)
	response, err := client.Unseal(t.Context(), &kms.Request{NodeUuid: id, Data: blob})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(response.Data, plaintext) {
		t.Error("nil gate returned plaintext")
	}
	if len(response.Data) != len(plaintext) {
		t.Error("failure response size changed")
	}
}
