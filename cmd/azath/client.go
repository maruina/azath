package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/google/uuid"
	kms "github.com/siderolabs/kms-client/api/kms"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/maruina/azath/internal/crypto"
)

// exitCodeError signals that the process should exit with a specific code.
type exitCodeError struct {
	code int
	msg  string
}

func (e *exitCodeError) Error() string {
	return e.msg
}

func newClientCmd() *cobra.Command {
	var (
		configPath  string
		endpoints   []string
		sealedBlob  string
		timeout     time.Duration
		insecureDev bool
	)

	cmd := &cobra.Command{
		Use:   "client --sealed-blob <path> -- <command> [args...]",
		Short: "Unseal a secret and append it as the final argument to a command",
		Long: `Client unseals a sealed blob via an azath KMS endpoint and appends
the plaintext passphrase as the final argument to the given command.

The sealed blob must be base64-encoded (as produced by "azath seal").
The command after "--" is executed without shell interpolation.

Example:
  azath client \
    --config /etc/azath/client.yaml \
    --sealed-blob /etc/azath/secrets/homes.key.sealed \
    -- /usr/syno/sbin/synoshare --enc_mount homes`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runClient(cmd.Context(), configPath, endpoints, sealedBlob, timeout, insecureDev, args); err != nil {
				var exitErr *exitCodeError
				if errors.As(err, &exitErr) {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(exitErr.code)
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "client config path")
	_ = cmd.MarkFlagRequired("config")
	cmd.Flags().StringSliceVar(&endpoints, "endpoint", nil, "gRPC endpoint(s); overrides or extends config endpoints")
	cmd.Flags().StringVar(&sealedBlob, "sealed-blob", "", "path to base64-encoded sealed blob")
	_ = cmd.MarkFlagRequired("sealed-blob")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "per-endpoint timeout")
	cmd.Flags().BoolVar(&insecureDev, "insecure-dev", false, "use plaintext h2c for localhost/tests only")
	return cmd
}

func runClient(ctx context.Context, configPath string, flagEndpoints []string, sealedBlobPath string, perEndpointTimeout time.Duration, insecureDev bool, commandArgs []string) error {
	// Load and validate client config.
	clientCfg, err := loadClientConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if _, parseErr := uuid.Parse(clientCfg.Device.UUID); parseErr != nil {
		return fmt.Errorf("client.device.uuid is not a valid UUID: %w", parseErr)
	}

	// Build endpoint list: flag endpoints come first (as overrides), then config endpoints.
	eps := make([]string, 0, len(flagEndpoints)+len(clientCfg.Endpoints))
	eps = append(eps, flagEndpoints...)
	eps = append(eps, clientCfg.Endpoints...)
	if len(eps) == 0 {
		return fmt.Errorf("at least one endpoint is required (from config or --endpoint)")
	}

	// Read and base64-decode the sealed blob.
	blobData, err := os.ReadFile(sealedBlobPath) // #nosec G304 — path comes from CLI flag
	if err != nil {
		return fmt.Errorf("reading sealed blob: %w", err)
	}
	sealedBytes, err := base64.StdEncoding.DecodeString(string(blobData))
	if err != nil {
		return fmt.Errorf("decoding sealed blob: %w", err)
	}

	// Try each endpoint in order.
	var lastErr error
	for _, ep := range eps {
		plaintext, err := tryUnseal(ctx, ep, clientCfg.Device.UUID, sealedBytes, perEndpointTimeout, insecureDev)
		if err != nil {
			lastErr = err
			continue
		}
		if len(plaintext) == 0 {
			lastErr = fmt.Errorf("endpoint %s returned empty plaintext", ep)
			continue
		}
		// Execute the command with plaintext as the final argument.
		exitCode, execErr := execCommand(ctx, commandArgs, plaintext)
		crypto.Zero(plaintext)
		if execErr != nil {
			return fmt.Errorf("command failed: %w", execErr)
		}
		if exitCode != 0 {
			return &exitCodeError{code: 2, msg: fmt.Sprintf("command exited with code %d", exitCode)}
		}
		return nil
	}

	return fmt.Errorf("all endpoints failed; last error: %w", lastErr)
}

// tryUnseal dials a single gRPC endpoint and calls Unseal.
func tryUnseal(ctx context.Context, endpoint string, deviceUUID string, sealedBytes []byte, perEndpointTimeout time.Duration, insecureDev bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, perEndpointTimeout)
	defer cancel()

	dialOpts := buildDialOptions(insecureDev)
	conn, err := grpc.NewClient(endpoint, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating gRPC client for %s: %w", endpoint, err)
	}
	defer func() { _ = conn.Close() }()

	client := kms.NewKMSServiceClient(conn)
	resp, err := client.Unseal(ctx, &kms.Request{
		NodeUuid: deviceUUID,
		Data:     sealedBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("unseal RPC failed at %s: %w", endpoint, err)
	}

	// Copy the plaintext so we can close the connection before using it.
	plaintext := make([]byte, len(resp.Data))
	copy(plaintext, resp.Data)
	return plaintext, nil
}

// execCommand runs the command with args plus plaintext as the final argument.
// It does not use a shell and does not log any arguments.
// Returns (exitCode, error). A non-zero exit code returns (code, nil).
func execCommand(ctx context.Context, args []string, plaintext []byte) (int, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("no command specified after --")
	}

	// Convert plaintext to string for the exec call. This is the only point
	// where key bytes leave the []byte domain — required because exec.Command
	// accepts string arguments. The underlying string backing store cannot be
	// zeroed; this is a documented limitation (see AGENTS.md: "Known limitation:
	// Go's AES expanded key schedule cannot be zeroed.").
	passphrase := string(plaintext)

	cmdArgs := make([]string, len(args)+1)
	copy(cmdArgs, args)
	cmdArgs[len(cmdArgs)-1] = passphrase

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...) // #nosec G204 — command comes from CLI args
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 0, fmt.Errorf("executing command: %w", err)
	}
	return 0, nil
}
