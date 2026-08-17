// Package config loads and validates redProxy's YAML configuration.
//
// Loading is strict: unknown fields are rejected and every route is
// validated at startup so the proxy fails fast with clear errors instead
// of misbehaving at runtime.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Strategy names a load-balancing strategy for a route.
type Strategy string

const (
	StrategyRoundRobin Strategy = "round_robin"
	StrategyLeastConn  Strategy = "least_conn"
	StrategyRandom     Strategy = "random"
)

// Duration is a time.Duration that unmarshals from YAML strings like "5s".
type Duration time.Duration

// D returns the duration as a standard time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"5s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Config is the root of the YAML configuration file.
type Config struct {
	Server ServerConfig  `yaml:"server"`
	Routes []RouteConfig `yaml:"routes"`
}

// ServerConfig configures the proxy and admin HTTP servers.
type ServerConfig struct {
	Listen       string   `yaml:"listen"`
	AdminListen  string   `yaml:"admin_listen"`
	ReadTimeout  Duration `yaml:"read_timeout"`
	WriteTimeout Duration `yaml:"write_timeout"`
	IdleTimeout  Duration `yaml:"idle_timeout"`
}

// RouteConfig maps incoming requests to a pool of backends.
type RouteConfig struct {
	Name        string            `yaml:"name"`
	Match       MatchConfig       `yaml:"match"`
	StripPrefix string            `yaml:"strip_prefix"`
	Strategy    Strategy          `yaml:"strategy"`
	Backends    []BackendConfig   `yaml:"backends"`
	HealthCheck HealthCheckConfig `yaml:"health_check"`
	Middlewares MiddlewareConfig  `yaml:"middlewares"`
}

// MatchConfig decides which requests a route serves. Host is optional;
// PathPrefix is required and longest prefix wins when routes overlap.
type MatchConfig struct {
	Host       string `yaml:"host"`
	PathPrefix string `yaml:"path_prefix"`
}

// BackendConfig is a single upstream server.
type BackendConfig struct {
	URL string `yaml:"url"`
}

// HealthCheckConfig configures active probing of a route's backends.
type HealthCheckConfig struct {
	Path     string   `yaml:"path"`
	Interval Duration `yaml:"interval"`
	Timeout  Duration `yaml:"timeout"`
}

// MiddlewareConfig holds the optional per-route middleware blocks.
// A nil pointer means the middleware is disabled for that route.
type MiddlewareConfig struct {
	RateLimit *RateLimitConfig `yaml:"rate_limit"`
	Headers   *HeadersConfig   `yaml:"headers"`
}

// RateLimitConfig is a per-client-IP token bucket.
type RateLimitConfig struct {
	RPS   float64 `yaml:"rps"`
	Burst int     `yaml:"burst"`
}

// HeadersConfig sets or removes headers on the request and/or response.
type HeadersConfig struct {
	RequestSet     map[string]string `yaml:"request_set"`
	RequestRemove  []string          `yaml:"request_remove"`
	ResponseSet    map[string]string `yaml:"response_set"`
	ResponseRemove []string          `yaml:"response_remove"`
}

// Load reads, parses, and validates the YAML config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	return Parse(data)
}

// Parse parses and validates YAML config data.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // reject unknown fields: catch typos early
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config:\n%w", err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = ":8080"
	}
	if c.Server.AdminListen == "" {
		c.Server.AdminListen = ":9090"
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = Duration(5 * time.Second)
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = Duration(30 * time.Second)
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = Duration(60 * time.Second)
	}

	for i := range c.Routes {
		r := &c.Routes[i]
		if r.Strategy == "" {
			r.Strategy = StrategyRoundRobin
		}
		if r.HealthCheck.Path == "" {
			r.HealthCheck.Path = "/"
		}
		if r.HealthCheck.Interval == 0 {
			r.HealthCheck.Interval = Duration(10 * time.Second)
		}
		if r.HealthCheck.Timeout == 0 {
			r.HealthCheck.Timeout = Duration(2 * time.Second)
		}
	}
}

// Validate checks the configuration for errors, reporting all problems
// found rather than stopping at the first one.
func (c *Config) Validate() error {
	var errs []error

	if len(c.Routes) == 0 {
		errs = append(errs, errors.New("config must define at least one route"))
	}

	seen := make(map[string]bool, len(c.Routes))
	for i := range c.Routes {
		r := &c.Routes[i]
		label := fmt.Sprintf("route %q", r.Name)
		if r.Name == "" {
			label = fmt.Sprintf("route at index %d", i)
			errs = append(errs, fmt.Errorf("%s: name is required", label))
		} else if seen[r.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate route name", label))
		}
		seen[r.Name] = true

		if r.Match.PathPrefix == "" {
			errs = append(errs, fmt.Errorf("%s: match.path_prefix is required", label))
		} else if !strings.HasPrefix(r.Match.PathPrefix, "/") {
			errs = append(errs, fmt.Errorf("%s: match.path_prefix must start with \"/\"", label))
		}

		switch r.Strategy {
		case StrategyRoundRobin, StrategyLeastConn, StrategyRandom:
		default:
			errs = append(errs, fmt.Errorf("%s: invalid strategy %q (want %q, %q, or %q)",
				label, r.Strategy, StrategyRoundRobin, StrategyLeastConn, StrategyRandom))
		}

		if r.StripPrefix != "" && !strings.HasPrefix(r.Match.PathPrefix, r.StripPrefix) {
			errs = append(errs, fmt.Errorf("%s: strip_prefix %q is not a prefix of match.path_prefix %q",
				label, r.StripPrefix, r.Match.PathPrefix))
		}

		if len(r.Backends) == 0 {
			errs = append(errs, fmt.Errorf("%s: at least one backend is required", label))
		}
		for _, b := range r.Backends {
			if err := validateBackendURL(b.URL); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", label, err))
			}
		}

		if r.HealthCheck.Interval <= 0 {
			errs = append(errs, fmt.Errorf("%s: health_check.interval must be positive", label))
		}
		if r.HealthCheck.Timeout <= 0 {
			errs = append(errs, fmt.Errorf("%s: health_check.timeout must be positive", label))
		}
		if r.HealthCheck.Interval > 0 && r.HealthCheck.Timeout >= r.HealthCheck.Interval {
			errs = append(errs, fmt.Errorf("%s: health_check.timeout (%s) must be less than interval (%s)",
				label, r.HealthCheck.Timeout.D(), r.HealthCheck.Interval.D()))
		}

		if rl := r.Middlewares.RateLimit; rl != nil {
			if rl.RPS <= 0 {
				errs = append(errs, fmt.Errorf("%s: rate_limit.rps must be positive", label))
			}
			if rl.Burst < 1 {
				errs = append(errs, fmt.Errorf("%s: rate_limit.burst must be at least 1", label))
			}
		}
	}

	return errors.Join(errs...)
}

func validateBackendURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid backend URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid backend URL %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid backend URL %q: missing host", raw)
	}
	return nil
}
