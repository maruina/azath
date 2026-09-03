package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/google/uuid"
	kms "github.com/siderolabs/kms-client/api/kms"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"gopkg.in/yaml.v3"

	"github.com/maruina/azath/internal/crypto"
	"github.com/maruina/azath/internal/fsutil"
)

const maxPlaintextSize = 1 << 20 // 1 MiB

func newSealCmd() *cobra.Command {
	var (
		configPath    string
		endpoint      []string
		sealTokenFile string
		outPath       string
		prompt        bool
		insecureDev   bool
	)

	cmd := &cobra.Command{
		Use:   "seal",
		Short: "Seal a secret against a KMS endpoint",
		Long: `Seal reads plaintext from stdin (or interactively with --prompt), calls the
KMS Seal RPC, and writes the base64-encoded sealed blob to stdout or to a file.

The seal token is read from --seal-token-file and used as a Bearer token in the
gRPC metadata. The device UUID from the client config identifies the target node.

Example:
  azath seal \
    --config /etc/azath/client.yaml \
    --seal-token-file /etc/azath/seal-token \
    --out /etc/azath/secrets/homes.key.sealed \
    --prompt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSeal(cmd.Context(), configPath, endpoint, sealTokenFile, outPath, prompt, insecureDev)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "client config path")
	cmd.Flags().StringSliceVar(&endpoint, "endpoint", nil, "gRPC endpoint(s); overrides config endpoints")
	_ = cmd.MarkFlagRequired("config")
	cmd.Flags().StringVar(&sealTokenFile, "seal-token-file", "", "path to seal bearer token file")
	_ = cmd.MarkFlagRequired("seal-token-file")
	cmd.Flags().StringVar(&outPath, "out", "", "output path; if omitted, write base64 to stdout")
	cmd.Flags().BoolVar(&prompt, "prompt", false, "read plaintext interactively with terminal echo disabled")
	cmd.Flags().BoolVar(&insecureDev, "insecure-dev", false, "use plaintext h2c for localhost/tests only")
	return cmd
}

func runSeal(ctx context.Context, configPath string, endpoints []string, sealTokenFile, outPath string, prompt, insecureDev bool) error {
	// Load and validate client config.
	clientCfg, err := loadClientConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if _, parseErr := uuid.Parse(clientCfg.Device.UUID); parseErr != nil {
		return fmt.Errorf("client.device.uuid is not a valid UUID: %w", parseErr)
	}
	if len(endpoints) == 0 {
		endpoints = clientCfg.Endpoints
	}
	if len(endpoints) == 0 {
		return errors.New("at least one endpoint is required (from config or --endpoint)")
	}

	// Read seal token from file.
	tokenBytes, err := os.ReadFile(sealTokenFile) // #nosec G304 — path comes from CLI flag
	if err != nil {
		return fmt.Errorf("reading seal token file: %w", err)
	}
	// Trim a single trailing newline if present.
	tokenBytes = bytes.TrimSuffix(tokenBytes, []byte("\n"))
	if len(tokenBytes) == 0 {
		return errors.New("seal token file is empty")
	}
	defer crypto.ZeroOnReturn(&tokenBytes)

	// Read plaintext.
	plaintext, err := readPlaintext(prompt)
	if err != nil {
		return err
	}
	if len(plaintext) == 0 {
		return errors.New("plaintext is empty")
	}
	defer crypto.ZeroOnReturn(&plaintext)

	// Build gRPC connection.
	target := endpoints[0]
	dialOpts := buildDialOptions(insecureDev)
	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return fmt.Errorf("creating gRPC client: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Call Seal RPC with bearer token.
	md := metadata.Pairs("authorization", "Bearer "+string(tokenBytes))
	callCtx := metadata.NewOutgoingContext(ctx, md)
	client := kms.NewKMSServiceClient(conn)

	resp, err := client.Seal(callCtx, &kms.Request{
		NodeUuid: clientCfg.Device.UUID,
		Data:     plaintext,
	})
	if err != nil {
		return fmt.Errorf("seal RPC failed: %w", err)
	}

	// Base64-encode the sealed response.
	encoded := base64.StdEncoding.EncodeToString(resp.Data)

	// Write output.
	if outPath != "" {
		if writeErr := fsutil.Write(outPath, []byte(encoded)); writeErr != nil {
			return fmt.Errorf("writing output: %w", writeErr)
		}
	} else {
		if _, printErr := fmt.Println(encoded); printErr != nil {
			return fmt.Errorf("writing to stdout: %w", printErr)
		}
	}

	return nil
}

// readPlaintext reads the secret from stdin or interactively via terminal.
func readPlaintext(prompt bool) ([]byte, error) {
	if !prompt {
		reader := io.LimitReader(os.Stdin, maxPlaintextSize+1)
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
		if len(data) > maxPlaintextSize {
			return nil, fmt.Errorf("stdin exceeds 1 MiB limit")
		}
		return data, nil
	}

	// Interactive prompt with echo disabled.
	if !term.IsTerminal(int(syscall.Stdin)) {
		return nil, errors.New("--prompt requires a terminal")
	}

	fmt.Fprint(os.Stderr, "Enter secret: ")
	first, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return nil, fmt.Errorf("reading password: %w", err)
	}
	fmt.Fprintln(os.Stderr)

	fmt.Fprint(os.Stderr, "Confirm secret: ")
	second, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		crypto.Zero(first)
		return nil, fmt.Errorf("reading password confirmation: %w", err)
	}
	fmt.Fprintln(os.Stderr)

	if !constantTimeEqual(first, second) {
		crypto.Zero(first)
		crypto.Zero(second)
		return nil, errors.New("passwords do not match")
	}
	crypto.Zero(second)

	if len(first) == 0 {
		crypto.Zero(first)
		return nil, errors.New("secret is empty")
	}

	return first, nil
}

// constantTimeEqual compares two byte slices in constant time.
func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var result byte
	for i := range a {
		result |= a[i] ^ b[i]
	}
	return result == 0
}

// buildDialOptions returns gRPC dial options based on security mode.
func buildDialOptions(insecureDev bool) []grpc.DialOption {
	if insecureDev {
		return []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}
	}
	// Enforce TLS 1.3 minimum for production use.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	return []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
	}
}

// clientConfig is a minimal config for azath client operations.
type clientConfig struct {
	Device    clientDeviceConfig `yaml:"device"`
	Endpoints []string           `yaml:"endpoints"`
}

type clientDeviceConfig struct {
	Name string `yaml:"name"`
	UUID string `yaml:"uuid"`
}

// loadClientConfig loads and validates a standalone client config file.
func loadClientConfig(path string) (*clientConfig, error) {
	data, err := os.ReadFile(path) // #nosec G304 — path comes from CLI flag
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg clientConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Device.Name == "" {
		return nil, errors.New("client.device.name is required")
	}
	if cfg.Device.UUID == "" {
		return nil, errors.New("client.device.uuid is required")
	}
	if len(cfg.Endpoints) == 0 {
		return nil, errors.New("client.endpoints must contain at least one endpoint")
	}

	return &cfg, nil
}
