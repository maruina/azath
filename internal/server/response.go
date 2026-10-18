package server

import (
	"crypto/rand"
	"fmt"

	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc/encoding"
	_ "google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/mem"

	"github.com/maruina/azath/internal/crypto"
)

func randomResponse(req *kms.Request) *kms.Response {
	n := len(req.GetData()) - crypto.MinSealedLen
	if n <= 0 {
		n = crypto.KeySize
	}
	data := make([]byte, n)
	_, _ = rand.Read(data)
	return &kms.Response{Data: data}
}

// responseCodec clears application-owned response data after protobuf has copied
// it into transport buffers. Transport, TLS, and client decode copies remain
// under gRPC and runtime ownership and cannot be reliably cleared here.
type responseCodec struct{ encoding.CodecV2 }

func (c responseCodec) Marshal(v any) (mem.BufferSlice, error) {
	if response, ok := v.(*kms.Response); ok {
		defer crypto.Zero(response.Data)
	}
	data, err := c.CodecV2.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	return data, nil
}
