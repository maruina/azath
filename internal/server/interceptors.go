package server

import (
	"context"
	"crypto/rand"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/maruina/azath/internal/crypto"
)

// panicRecoveryInterceptor is the outermost interceptor. It catches panics in
// the handler and keeps the server alive. For Unseal, it returns codes.OK with
// random bytes to preserve the oracle contract — a panic must not be
// distinguishable from a gate denial or decrypt error. For all other methods,
// it returns codes.Internal.
func panicRecoveryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ctx, "panic in gRPC handler",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(debug.Stack()),
				)
				if strings.HasSuffix(info.FullMethod, "/Unseal") {
					// Oracle contract: Unseal must always return codes.OK + random bytes.
					// A panic (e.g. buggy Gate implementation) must not produce a
					// distinguishable response code.
					buf := make([]byte, crypto.KeySize)
					_, _ = rand.Read(buf)
					resp = &kms.Response{Data: buf}
					err = nil
				} else {
					err = status.Errorf(codes.Internal, "internal server error")
				}
			}
		}()
		return handler(ctx, req)
	}
}

// loggingInterceptor logs each RPC with method name, abbreviated node UUID,
// duration, and gRPC status code. Log level varies by outcome:
// OK → Info, non-OK → Warn, Internal/Unknown → Error.
func loggingInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err)

		level := slog.LevelInfo
		if code != codes.OK {
			level = slog.LevelWarn
		}
		if code == codes.Internal || code == codes.Unknown || code == codes.DataLoss {
			level = slog.LevelError
		}

		// Pre-allocate for 3 fixed attrs + 1 optional (node_uuid_prefix).
		attrs := make([]slog.Attr, 0, 4)
		attrs = append(attrs,
			slog.String("method", info.FullMethod),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("code", code.String()),
		)
		if kmsReq, ok := req.(*kms.Request); ok && len(kmsReq.GetNodeUuid()) >= 8 {
			attrs = append(attrs, slog.String("node_uuid_prefix", kmsReq.GetNodeUuid()[:8]))
		}

		logger.LogAttrs(ctx, level, "rpc", attrs...)
		return resp, err
	}
}
