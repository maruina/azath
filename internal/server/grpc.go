package server

import (
	"log/slog"

	kms "github.com/siderolabs/kms-client/api/kms"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/maruina/azath/internal/gate"
)

// Gate is an alias for gate.Gate for backward compatibility.
type Gate = gate.Gate

// GateResult is an alias for gate.Decision for backward compatibility.
type GateResult = gate.Decision

const (
	GateDenied   = gate.Denied
	GateApproved = gate.Approved
	GatePending  = gate.Pending
)

// NewGRPCServer creates a grpc.Server with the KMS service and health service
// registered. The following options are always applied:
//   - MaxRecvMsgSize: 1 MiB
//   - panic recovery interceptor (outermost)
//   - logging interceptor
//
// extraOpts are appended after the mandatory options so callers can add
// TLS credentials, keepalive parameters, etc. without overriding the
// mandatory options.
//
// The returned *health.Server is set to SERVING and can be updated by the
// caller (e.g. set to NOT_SERVING during graceful shutdown).
func NewGRPCServer(srv *KMSServer, logger *slog.Logger, extraOpts ...grpc.ServerOption) (*grpc.Server, *health.Server) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	chainOpt := grpc.ChainUnaryInterceptor(
		panicRecoveryInterceptor(logger),
		loggingInterceptor(logger),
	)

	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(1 << 20),
		chainOpt,
	}
	opts = append(opts, extraOpts...)

	gs := grpc.NewServer(opts...)

	kms.RegisterKMSServiceServer(gs, srv)

	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(gs, hs)

	return gs, hs
}
