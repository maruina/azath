package server

import (
	"bytes"
	"strconv"
	"testing"

	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc/encoding"
	_ "google.golang.org/grpc/encoding/proto"
)

func TestResponseCodecClearsSourceAfterMarshal(t *testing.T) {
	t.Parallel()
	for _, size := range []int{17, 8192} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()
			source := bytes.Repeat([]byte{0x9a}, size)
			response := &kms.Response{Data: source}
			codec := responseCodec{CodecV2: encoding.GetCodecV2("proto")}
			wire, err := codec.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			defer wire.Free()
			if !bytes.Equal(source, make([]byte, size)) {
				t.Error("source was not cleared")
			}
			var decoded kms.Response
			if err := encoding.GetCodecV2("proto").Unmarshal(wire, &decoded); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(decoded.Data, bytes.Repeat([]byte{0x9a}, size)) {
				t.Error("wire plaintext changed")
			}
		})
	}
}
