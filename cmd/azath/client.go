package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/maruina/azath/internal/crypto"
	kms "github.com/siderolabs/kms-client/api/kms"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

type mountShare func(context.Context, string, []byte) error

func newSynologyCmd() *cobra.Command { return newSynologyCmdWithMount(mountSynologyShare) }

func newSynologyCmdWithMount(mount mountShare) *cobra.Command {
	var configPath string
	var insecureDev bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "synology --config <path>", Short: "Unlock configured Synology encrypted shares sequentially",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if timeout <= 0 || timeout > 30*time.Minute {
				return errors.New("timeout must be positive and at most 30m")
			}
			cfg, err := loadClientConfig(configPath)
			if err != nil {
				return err
			}
			if len(cfg.Shares) == 0 {
				return errors.New("shares must contain at least one share")
			}
			failed := false
			for _, share := range cfg.Shares {
				if err := cmd.Context().Err(); err != nil {
					return fmt.Errorf("synology canceled: %w", err)
				}
				result := "mounted"
				blob, err := readBlob(share.SealedBlob)
				if err != nil {
					result = "invalid_blob"
				} else {
					var plaintext []byte
					for _, endpoint := range cfg.Endpoints {
						if cmd.Context().Err() != nil {
							break
						}
						plaintext, err = tryUnseal(cmd.Context(), endpoint, cfg.Device.UUID, blob, timeout, insecureDev)
						if err == nil && len(plaintext) != 0 {
							break
						}
					}
					switch {
					case err != nil || len(plaintext) == 0:
						result = "unseal_failed"
					case bytes.IndexByte(plaintext, 0) >= 0 || len(plaintext) > 4096:
						result = "invalid_passphrase"
					case cmd.Context().Err() != nil:
						result = "canceled"
					default:
						mountCtx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
						if mount(mountCtx, share.Name, plaintext) != nil {
							result = "mount_failed"
						}
						cancel()
					}
					crypto.Zero(plaintext)
				}
				if result != "mounted" {
					failed = true
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", share.Name, result); err != nil {
					return fmt.Errorf("writing share result: %w", err)
				}
			}
			if failed {
				return errors.New("one or more shares failed")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "shared client config path")
	_ = cmd.MarkFlagRequired("config")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "per-endpoint approval deadline (maximum 30m)")
	cmd.Flags().BoolVar(&insecureDev, "insecure-dev", false, "use plaintext h2c for local tests")
	return cmd
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- explicit configuration-owned path
	if err != nil {
		return nil, errors.New("cannot open input file")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("input unreadable or exceeds size limit")
	}
	return data, nil
}

func readBlob(path string) ([]byte, error) {
	data, err := readBounded(path, 16<<10)
	if err != nil {
		return nil, err
	}
	blob, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(data)))
	if err != nil || len(blob) < 32 || len(blob) > 4128 {
		return nil, errors.New("invalid sealed blob")
	}
	return blob, nil
}

func mountSynologyShare(ctx context.Context, name string, plaintext []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mount canceled: %w", err)
	}
	// deliberate: DSM requires an argv string, which Go cannot reliably zero. A DSM stdin API would remove this copy.
	cmd := exec.CommandContext(ctx, "/usr/syno/sbin/synoshare", "--enc_mount", name, string(plaintext)) // #nosec G204 -- fixed executable and validated share name
	// Do not attach stdout/stderr: DSM diagnostics can contain the passphrase.
	if cmd.Run() != nil {
		return errors.New("mount failed")
	}
	return nil
}

// tryUnseal transfers ownership of the decoded plaintext to its caller.
func tryUnseal(ctx context.Context, endpoint string, deviceUUID string, sealedBytes []byte, perEndpointTimeout time.Duration, insecureDev bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, perEndpointTimeout)
	defer cancel()
	target, err := endpointTarget(endpoint)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(target, buildDialOptions(insecureDev)...)
	if err != nil {
		return nil, fmt.Errorf("creating gRPC client: %w", err)
	}
	defer func() { _ = conn.Close() }()
	resp, err := kms.NewKMSServiceClient(conn).Unseal(ctx, &kms.Request{NodeUuid: deviceUUID, Data: sealedBytes})
	if err != nil {
		return nil, fmt.Errorf("unseal RPC failed: %w", err)
	}
	return resp.Data, nil
}
