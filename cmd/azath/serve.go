package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
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
	"github.com/maruina/azath/internal/secret"
	"github.com/maruina/azath/internal/server"
)

// masterKeyLoadTimeout is the maximum time allowed for the key source to
// return the master key. Slow local I/O must not block startup indefinitely.
const masterKeyLoadTimeout = 60 * time.Second

func newServeCmd() *cobra.Command {
	var configPath string
	var dev bool
	var telegramAPIBase string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the gRPC KMS server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd.Context(), configPath, dev, telegramAPIBase)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "/etc/azath/server.yaml", "path to config file")
	cmd.Flags().BoolVar(&dev, "dev", false, "development mode: text logging, skip mlock")
	cmd.Flags().StringVar(&telegramAPIBase, "telegram-api-base", "", "Telegram API base URL for testing; requires --dev")
	return cmd
}

func runServe(ctx context.Context, configPath string, dev bool, telegramAPIBase string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	var gateOptions []gate.Option
	if telegramAPIBase != "" {
		if !dev {
			return errors.New("--telegram-api-base requires --dev")
		}
		u, err := url.Parse(telegramAPIBase)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("--telegram-api-base must be an HTTP or HTTPS origin without credentials, path, query, or fragment")
		}
		gateOptions = append(gateOptions, gate.WithAPIBase(telegramAPIBase))
	}
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

		gateOptions = append(gateOptions, gate.WithPromptRateLimit(vc.TelegramRateLimit))
		g, err = gate.NewTelegramGate(
			cfg.Server.Name,
			botToken,
			cfg.Notifications.Telegram.ChatID,
			vc.TelegramAuthorizedUserID,
			approvalTTL,
			vc.TelegramApprovalCacheTTL,
			logger,
			gateOptions...,
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

	kmsSrv := server.New(km, metrics,
		server.WithSealToken(sealToken),
		server.WithGate(g),
		server.WithDevices(devices),
		server.WithLogger(logger),
	)
	defer kmsSrv.Close()
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
		slog.Int("devices", len(devices)),
		slog.Bool("mlock_addr_locked", mlocked), // reflects mlockall only; RLIMIT_CORE logged separately on failure
		slog.Bool("dev", dev),
	)

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-srvErr:
		logger.Error("server error, initiating shutdown", slog.Any("error", serveErr))
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

	logger.Info("goodbye")
	return serveErr
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
