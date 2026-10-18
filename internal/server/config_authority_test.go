package server_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc/metadata"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/gate"
	"github.com/maruina/azath/internal/server"
)

type nameCheckingGate struct {
	t          *testing.T
	name, uuid string
}

func (g *nameCheckingGate) Check(_ context.Context, d gate.Device) (gate.Decision, error) {
	if d.Name != g.name || d.UUID != g.uuid {
		g.t.Errorf("gate device = %+v, want %s/%s", d, g.name, g.uuid)
	}
	return gate.Approved, nil
}
func (*nameCheckingGate) Close() error { return nil }

func TestConfigRenamePreservesExistingBlob(t *testing.T) {
	t.Parallel()
	m := newTestMetrics(t)
	mgr := newLoadedManager(t, testKey(), m)
	id := uuid.NewString()
	plaintext := []byte("config-only device key")
	defer crypto.Zero(plaintext)
	first := server.New(mgr, m, server.WithSealToken([]byte(testSealToken)), server.WithDevices(map[string]server.DeviceInfo{id: {Name: "old-name", UUID: id}}))
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+testSealToken))
	sealed, err := first.Seal(ctx, &kms.Request{NodeUuid: id, Data: plaintext})
	first.Close()
	if err != nil {
		t.Fatal(err)
	}
	renamed := server.New(mgr, m, server.WithGate(&nameCheckingGate{t: t, name: "new-name", uuid: id}), server.WithDevices(map[string]server.DeviceInfo{id: {Name: "new-name", UUID: id}}))
	defer renamed.Close()
	response, err := renamed.Unseal(t.Context(), &kms.Request{NodeUuid: id, Data: sealed.Data})
	if err != nil {
		t.Fatal(err)
	}
	defer crypto.Zero(response.Data)
	if !bytes.Equal(response.Data, plaintext) {
		t.Fatal("config rename prevented unsealing existing blob")
	}
}
