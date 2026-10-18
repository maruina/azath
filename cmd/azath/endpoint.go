package main

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// endpointTarget separates the HTTPS URL contract from gRPC resolver syntax.
func endpointTarget(endpoint string) (string, error) {
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
