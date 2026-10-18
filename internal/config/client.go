package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

var shareNameRegex = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_. -]{0,79}$`)

// ShareConfig defines a single share to unlock.
type ShareConfig struct {
	Name       string `yaml:"name"`
	SealedBlob string `yaml:"sealed_blob"`
}

// ClientConfig holds client configuration for seal and synology commands.
type ClientConfig struct {
	Shares    []ShareConfig      `yaml:"shares"`
	Device    ClientDeviceConfig `yaml:"device"`
	Endpoints []string           `yaml:"endpoints"`
}

// ClientDeviceConfig holds device identity in client configuration.
type ClientDeviceConfig struct {
	Name string `yaml:"name"`
	UUID string `yaml:"uuid"`
}

// EndpointTarget separates the HTTPS URL contract from gRPC resolver syntax.
func EndpointTarget(endpoint string) (string, error) {
	target := endpoint
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return "", fmt.Errorf("endpoint must be an HTTPS URL with only a host and port")
		}
		target = u.Host
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" || strings.ContainsAny(host, "/?#@ \\") {
		return "", fmt.Errorf("endpoint must contain a host and port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("endpoint port must be between 1 and 65535")
	}
	return net.JoinHostPort(host, port), nil
}

// LoadClientConfig reads and validates a client configuration file.
func LoadClientConfig(path string) (*ClientConfig, error) {
	data, err := readBoundedFile(path, 64<<10)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return LoadClientConfigFromBytes(data)
}

// LoadClientConfigFromBytes parses and validates raw client config YAML bytes.
func LoadClientConfigFromBytes(data []byte) (*ClientConfig, error) {
	var cfg ClientConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if decodeErr := dec.Decode(&cfg); decodeErr != nil {
		return nil, fmt.Errorf("parsing config: %w", decodeErr)
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

	if decodeErr := dec.Decode(new(any)); !errors.Is(decodeErr, io.EOF) {
		return nil, errors.New("config must contain exactly one YAML document")
	}
	parsed, err := uuid.Parse(cfg.Device.UUID)
	if err != nil || parsed.String() != cfg.Device.UUID {
		return nil, errors.New("device UUID must be canonical")
	}
	if len(cfg.Device.Name) > 128 || len(cfg.Endpoints) > 8 || len(cfg.Shares) > 64 {
		return nil, errors.New("config exceeds input limits")
	}
	for _, endpoint := range cfg.Endpoints {
		if len(endpoint) > 2048 {
			return nil, errors.New("endpoint too long")
		}
		if _, err := EndpointTarget(endpoint); err != nil {
			return nil, err
		}
	}
	seen := map[string]struct{}{}
	for _, share := range cfg.Shares {
		if !shareNameRegex.MatchString(share.Name) || len(share.SealedBlob) == 0 || len(share.SealedBlob) > 4096 || strings.ContainsRune(share.SealedBlob, 0) {
			return nil, errors.New("invalid share name or blob path")
		}
		if _, ok := seen[share.Name]; ok {
			return nil, errors.New("duplicate share name")
		}
		seen[share.Name] = struct{}{}
	}
	return &cfg, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304
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
