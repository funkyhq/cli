// Package auth logs the CLI in to Funky Cloud as a user: a browser login
// that leaves a long-lived CLI credential on this machine, traded for
// short-lived access tokens as commands need them.
package auth

import (
	"os"
	"strings"
)

// Endpoints are the Funky Cloud services the CLI talks to.
type Endpoints struct {
	// API serves /v1/* (agent configs, tasks, sessions).
	API string
	// Console serves /console/v1/* (login, CLI sessions).
	Console string
	// Platform is the web app whose /cli/authorize page approves a login.
	Platform string
}

const (
	DefaultAPIURL      = "https://api.funky.dev"
	DefaultPlatformURL = "https://platform.funky.dev"
)

// EndpointsFromEnv reads FUNKY_API_URL, FUNKY_CONSOLE_URL and
// FUNKY_PLATFORM_URL, for developing against non-production services. In
// production the API and console share one host.
func EndpointsFromEnv() Endpoints {
	api := envOr("FUNKY_API_URL", DefaultAPIURL)
	return Endpoints{
		API:      api,
		Console:  envOr("FUNKY_CONSOLE_URL", api),
		Platform: envOr("FUNKY_PLATFORM_URL", DefaultPlatformURL),
	}
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return fallback
}
