package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/maruina/azath/internal/config"
	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/gate"
	"github.com/maruina/azath/internal/keymanager"
	"github.com/maruina/azath/internal/observability"
	"github.com/maruina/azath/internal/platform"
	"github.com/maruina/azath/internal/registry"
	"github.com/maruina/azath/internal/secret"
	"github.com/maruina/azath/internal/server"
)

// masterKeyLoadTimeout is the maximum time allowed for the key source to
// return the master key. Slow local I/O must not block startup indefinitely.
const masterKeyLoadTimeout = 60 * time.Second

func newServeCmd() *cobra.Command {
	var configPath string
	var dev bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the gRPC KMS server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd.Context(), configPath, dev)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "/etc/azath/server.yaml", "path to config file")
	cmd.Flags().BoolVar(&dev, "dev", false, "development mode: text logging, skip mlock")
	return cmd
}

func runServe(ctx context.Context, configPath string, dev bool) error {
	// mlock runs before config is loaded; use a plain stderr logger as bootstrap.
	var mlocked bool
	if !dev {
		bootstrapLogger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		mlocked = platform.LockMemory(bootstrapLogger)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if dev {
		cfg.Server.LogFormat = "text"
	}

	logger, err := observability.NewLogger(cfg.Server.LogLevel, cfg.Server.LogFormat)
	if err != nil {
		return fmt.Errorf("creating logger: %w", err)
	}

	metrics := observability.NewMetrics()

	vc, err := config.Validate(cfg)
	if err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	src, err := newKeySource(cfg)
	if err != nil {
		return fmt.Errorf("constructing key source: %w", err)
	}

	km := keymanager.New(metrics, logger)
	defer km.Destroy() // safety net if runServe returns early

	keyCtx, keyCancel := context.WithTimeout(ctx, masterKeyLoadTimeout)
	defer keyCancel()
	if err = km.Load(keyCtx, src); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			logger.Error("master key load timed out",
				slog.Duration("deadline", masterKeyLoadTimeout))
		} else {
			logger.Error("master key load failed", slog.Any("error", err))
		}
		return fmt.Errorf("loading master key: %w", err)
	}

	// The derived HMAC key is transferred to the registry, which holds it for
	// the process lifetime. Do not defer-zero it — the registry stores the
	// original slice, and zeroing it would break future persist operations.
	hmacKey, err := km.DeriveKey("azath-registry-hmac-v1", 32)
	if err != nil {
		return fmt.Errorf("deriving HMAC key: %w", err)
	}

	// registry.Load stores hmacKey directly without copying (see comment above).
	reg, err := registry.Load(cfg.Registry.Path, hmacKey, metrics, logger)
	if err != nil {
		crypto.Zero(hmacKey) // load failed; registry did not take ownership
		return fmt.Errorf("loading registry: %w", err)
	}

	// Build config-derived device lookup table: UUID -> deviceInfo.
	devices := make(map[string]server.DeviceInfo, len(cfg.Devices))
	for _, d := range cfg.Devices {
		parsed, _ := uuid.Parse(d.UUID) // already validated by config.Validate
		devices[parsed.String()] = server.DeviceInfo{
			Name:     d.Name,
			UUID:     parsed.String(),
			Disabled: d.Disabled,
		}
	}

	// Reconcile configured devices into the registry. This ensures every
	// configured device has a registry entry while rejecting UUID/name conflicts
	// between config and registry.
	for uuidStr, dev := range devices {
		entry, lookupErr := reg.Lookup(uuidStr)
		switch {
		case errors.Is(lookupErr, registry.ErrDeviceNotFound):
			// Not in registry yet — register it with name from config.
			if regErr := reg.Register(dev.Name, uuidStr); regErr != nil {
				return fmt.Errorf("registering configured device %s: %w", uuidStr, regErr)
			}
		case lookupErr != nil:
			return fmt.Errorf("looking up configured device %s: %w", uuidStr, lookupErr)
		default:
			if entry.Name != dev.Name {
				return fmt.Errorf("configured device %s name conflict: registry has %q, config has %q", uuidStr, entry.Name, dev.Name)
			}
		}
	}

	var resolver secret.Resolver
	if dev {
		resolver = secret.EnvResolver{}
	} else {
		resolver = secret.OPResolver{}
	}
	sealToken, err := resolver.Resolve(ctx, cfg.Server.SealTokenRef)
	if err != nil {
		return fmt.Errorf("resolving seal token: %w", err)
	}
	defer crypto.ZeroOnReturn(&sealToken)

	// Initialize Telegram gate if configured.
	var g gate.Gate
	if cfg.Gate != nil && cfg.Gate.Type == config.GateTypeTelegram {
		if cfg.Notifications.Telegram == nil {
			return errors.New("notifications.telegram is required for telegram gate")
		}
		var botToken []byte
		botToken, err = resolver.Resolve(ctx, cfg.Notifications.Telegram.BotTokenRef)
		if err != nil {
			return fmt.Errorf("resolving bot token: %w", err)
		}
		defer crypto.ZeroOnReturn(&botToken)

		approvalTTL, parseErr := time.ParseDuration(cfg.Notifications.Telegram.ApprovalTTL)
		if parseErr != nil {
			return fmt.Errorf("parsing approval TTL: %w", parseErr)
		}

		g, err = gate.NewTelegramGate(
			cfg.Server.Name,
			botToken,
			cfg.Notifications.Telegram.ChatID,
			vc.TelegramAuthorizedUserID,
			approvalTTL,
			vc.TelegramApprovalCacheTTL,
		)
		if err != nil {
			return fmt.Errorf("creating telegram gate: %w", err)
		}
		defer func() {
			if closeErr := g.Close(); closeErr != nil {
				logger.Warn("closing telegram gate", slog.Any("error", closeErr))
			}
		}()
	}

	kmsSrv := server.New(km, reg, metrics,
		server.WithSealToken(sealToken),
		server.WithGate(g),
		server.WithDevices(devices),
		server.WithLogger(logger),
	)
	// KMSServer copies the token; zero our local slice immediately.
	crypto.Zero(sealToken)

	// Plaintext gRPC on loopback — Caddy terminates TLS externally.
	grpcSrv, healthSrv := server.NewGRPCServer(kmsSrv, logger)

	hc := observability.NewHealthChecker(logger)
	hc.Register("master_key_loaded", func(_ context.Context) error {
		if !km.Loaded() {
			return errors.New("master key not loaded")
		}
		return nil
	})
	httpSrv := observability.NewHTTPServer(cfg.Server.MetricsListen, hc, metrics.Registry)

	// Open the listener before starting goroutines so a bind error is returned
	// immediately rather than discovered asynchronously.
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.Server.Listen, err)
	}

	// srvErr receives the first unexpected error from either server goroutine,
	// allowing a startup crash to trigger graceful shutdown rather than leaving
	// the process in a zombie state.
	srvErr := make(chan error, 2)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- fmt.Errorf("metrics/health server: %w", err)
		}
	}()
	go func() {
		if err := grpcSrv.Serve(lis); err != nil && !isGRPCStopError(err) {
			srvErr <- fmt.Errorf("gRPC server: %w", err)
		}
	}()

	logger.Info("azath started",
		slog.String("version", version),
		slog.String("commit", commit),
		slog.String("listen", cfg.Server.Listen),
		slog.String("key_source", string(cfg.MasterKey.Source)),
		slog.Int("devices", reg.Len()),
		slog.Bool("mlock_addr_locked", mlocked), // reflects mlockall only; RLIMIT_CORE logged separately on failure
		slog.Bool("dev", dev),
	)

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	select {
	case <-sigCtx.Done():
	case err := <-srvErr:
		logger.Error("server error, initiating shutdown", slog.Any("error", err))
		stop()
	}

	logger.Info("shutting down")

	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)

	grpcDone := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(grpcDone)
	}()
	grpcTimer := time.NewTimer(30 * time.Second)
	defer grpcTimer.Stop()
	select {
	case <-grpcDone:
	case <-grpcTimer.C:
		logger.Warn("gRPC graceful stop timed out, forcing stop")
		grpcSrv.Stop()
		<-grpcDone // wait for GracefulStop goroutine to exit before proceeding
	}

	// ctx is already cancelled at this point; use a fresh root context for the drain.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		logger.Warn("HTTP server shutdown error", slog.Any("error", err))
	}

	kmsSrv.Close()
	km.Destroy()

	logger.Info("goodbye")
	return nil
}

// isGRPCStopError reports whether err is the benign closed-listener error
// returned by grpc.Server.Serve after GracefulStop or Stop.
func isGRPCStopError(err error) bool {
	return errors.Is(err, net.ErrClosed)
}

// newKeySource constructs the local file-backed KeySource for the given config.
func newKeySource(cfg *config.Config) (keymanager.KeySource, error) {
	if cfg.MasterKey.Source != config.MasterKeySourceFile {
		return nil, fmt.Errorf("master_key.source %q: unsupported", cfg.MasterKey.Source)
	}
	return keymanager.NewFileSource(cfg.MasterKey.Path), nil
}
