package main

import (
	"github.com/maruina/azath/internal/config"
)

// endpointTarget separates the HTTPS URL contract from gRPC resolver syntax.
func endpointTarget(endpoint string) (string, error) {
	return config.EndpointTarget(endpoint)
}
