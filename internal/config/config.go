package config

import (
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	yaml "gopkg.in/yaml.v2"

	"gitlab.com/gitlab-org/gitlab-shell/client"
	"gitlab.com/gitlab-org/gitlab-shell/internal/metrics"
)

const (
	configFile            = "config.yml"
	defaultSecretFileName = ".gitlab_shell_secret"
)

type ServerConfig struct {
	Listen                  string   `yaml:"listen,omitempty"`
	ProxyProtocol           bool     `yaml:"proxy_protocol,omitempty"`
	WebListen               string   `yaml:"web_listen,omitempty"`
	ConcurrentSessionsLimit int64    `yaml:"concurrent_sessions_limit,omitempty"`
	GracePeriodSeconds      uint64   `yaml:"grace_period"`
	ReadinessProbe          string   `yaml:"readiness_probe"`
	LivenessProbe           string   `yaml:"liveness_probe"`
	HostKeyFiles            []string `yaml:"host_key_files,omitempty"`
}

type HttpSettingsConfig struct {
	User               string `yaml:"user"`
	Password           string `yaml:"password"`
	ReadTimeoutSeconds uint64 `yaml:"read_timeout"`
	CaFile             string `yaml:"ca_file"`
	CaPath             string `yaml:"ca_path"`
	SelfSignedCert     bool   `yaml:"self_signed_cert"`
}

type Config struct {
	User                  string `yaml:"user,omitempty"`
	RootDir               string
	LogFile               string `yaml:"log_file,omitempty"`
	LogFormat             string `yaml:"log_format,omitempty"`
	GitlabUrl             string `yaml:"gitlab_url"`
	GitlabRelativeURLRoot string `yaml:"gitlab_relative_url_root"`
	GitlabTracing         string `yaml:"gitlab_tracing"`
	// SecretFilePath is only for parsing. Application code should always use Secret.
	SecretFilePath string             `yaml:"secret_file"`
	Secret         string             `yaml:"secret"`
	SslCertDir     string             `yaml:"ssl_cert_dir"`
	HttpSettings   HttpSettingsConfig `yaml:"http_settings"`
	Server         ServerConfig       `yaml:"sshd"`

	httpClient     *client.HttpClient
	httpClientErr  error
	httpClientOnce sync.Once
}

// The defaults to apply before parsing the config file(s).
var (
	DefaultConfig = Config{
		LogFile:   "gitlab-shell.log",
		LogFormat: "json",
		Server:    DefaultServerConfig,
		User:      "git",
	}

	DefaultServerConfig = ServerConfig{
		Listen:                  "[::]:22",
		WebListen:               "localhost:9122",
		ConcurrentSessionsLimit: 10,
		GracePeriodSeconds:      10,
		ReadinessProbe:          "/start",
		LivenessProbe:           "/health",
		HostKeyFiles: []string{
			"/run/secrets/ssh-hostkeys/ssh_host_rsa_key",
			"/run/secrets/ssh-hostkeys/ssh_host_ecdsa_key",
			"/run/secrets/ssh-hostkeys/ssh_host_ed25519_key",
		},
	}
)

func (sc *ServerConfig) GracePeriod() time.Duration {
	return time.Duration(sc.GracePeriodSeconds) * time.Second
}

func (c *Config) ApplyGlobalState() {
	if c.SslCertDir != "" {
		os.Setenv("SSL_CERT_DIR", c.SslCertDir)
	}
}

func (c *Config) HttpClient() (*client.HttpClient, error) {
	c.httpClientOnce.Do(func() {
		client, err := client.NewHTTPClientWithOpts(
			c.GitlabUrl,
			c.GitlabRelativeURLRoot,
			c.HttpSettings.CaFile,
			c.HttpSettings.CaPath,
			c.HttpSettings.SelfSignedCert,
			c.HttpSettings.ReadTimeoutSeconds,
			nil,
		)
		if err != nil {
			c.httpClientErr = err
			return
		}

		tr := client.Transport
		client.Transport = promhttp.InstrumentRoundTripperDuration(metrics.HttpRequestDuration, tr)

		c.httpClient = client
	})

	return c.httpClient, c.httpClientErr
}

// NewFromDirExternal returns a new config from a given root dir. It also applies defaults appropriate for
// gitlab-shell running in an external SSH server.
func NewFromDirExternal(dir string) (*Config, error) {
	cfg, err := newFromFile(filepath.Join(dir, configFile))
	if err != nil {
		return nil, err
	}

	cfg.ApplyGlobalState()

	return cfg, nil
}

// NewFromDir returns a new config given a root directory. It looks for the config file name in the
// given directory and reads the config from it. It doesn't apply any defaults. New code should prefer
// this over NewFromDirIntegrated and apply the right default via one of the Apply... functions.
func NewFromDir(dir string) (*Config, error) {
	return newFromFile(filepath.Join(dir, configFile))
}

// newFromFile reads a new Config instance from the given file path. It doesn't apply any defaults.
func newFromFile(path string) (*Config, error) {
	cfg := &Config{}
	*cfg = DefaultConfig
	cfg.RootDir = filepath.Dir(path)

	configBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if err := yaml.Unmarshal(configBytes, cfg); err != nil {
		return nil, err
	}

	if cfg.GitlabUrl != "" {
		// This is only done for historic reasons, don't implement it for new config sources.
		unescapedUrl, err := url.PathUnescape(cfg.GitlabUrl)
		if err != nil {
			return nil, err
		}

		cfg.GitlabUrl = unescapedUrl
	}

	if err := parseSecret(cfg); err != nil {
		return nil, err
	}

	if len(cfg.LogFile) > 0 && cfg.LogFile[0] != '/' && cfg.RootDir != "" {
		cfg.LogFile = filepath.Join(cfg.RootDir, cfg.LogFile)
	}

	return cfg, nil
}

func parseSecret(cfg *Config) error {
	// The secret was parsed from yaml no need to read another file
	if cfg.Secret != "" {
		return nil
	}

	if cfg.SecretFilePath == "" {
		cfg.SecretFilePath = defaultSecretFileName
	}

	if !filepath.IsAbs(cfg.SecretFilePath) {
		cfg.SecretFilePath = path.Join(cfg.RootDir, cfg.SecretFilePath)
	}

	secretFileContent, err := os.ReadFile(cfg.SecretFilePath)
	if err != nil {
		return err
	}
	cfg.Secret = string(secretFileContent)

	return nil
}

// IsSane checks if the given config fulfills the minimum requirements to be able to run.
// Any error returned by this function should be a startup error. On the other hand
// if this function returns nil, this doesn't guarantee the config will work, but it's
// at least worth a try.
func (cfg *Config) IsSane() error {
	if cfg.GitlabUrl == "" {
		return errors.New("gitlab_url is required")
	}
	if cfg.Secret == "" {
		return errors.New("secret or secret_file_path is required")
	}
	return nil
}
