// Package etcdsource reads immutable pemcast bundles and active pointers from etcd.
package etcdsource

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
)

const maxBundleFileBytes = 4 << 20

// Client wraps the official etcd v3 client with pemcast protocol paths.
type Client struct {
	client         *clientv3.Client
	rootPrefix     string
	requestTimeout time.Duration
}

// New creates an etcd client. It does not require the cluster to be reachable yet.
func New(cfg config.Etcd, rootPrefix string) (*Client, error) {
	tlsConfig, err := buildTLSConfig(cfg.TLS)
	if err != nil {
		return nil, err
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   append([]string(nil), cfg.Endpoints...),
		Username:    cfg.Username,
		Password:    cfg.Password,
		DialTimeout: cfg.DialTimeout,
		TLS:         tlsConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("create etcd client: %w", err)
	}
	return newClient(client, rootPrefix, cfg.RequestTimeout), nil
}

func newClient(client *clientv3.Client, rootPrefix string, requestTimeout time.Duration) *Client {
	return &Client{client: client, rootPrefix: cleanRoot(rootPrefix), requestTimeout: requestTimeout}
}

func buildTLSConfig(cfg config.EtcdTLS) (*tls.Config, error) {
	enabled := cfg.CAFile != "" || cfg.CertFile != "" || cfg.KeyFile != "" || cfg.ServerName != "" || cfg.InsecureSkipVerify
	if !enabled {
		return nil, nil
	}
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         cfg.ServerName,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // Explicit operator configuration.
	}
	if cfg.CAFile != "" {
		contents, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read etcd CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(contents) {
			return nil, fmt.Errorf("etcd CA file contains no certificates")
		}
		tlsConfig.RootCAs = pool
	}
	if cfg.CertFile != "" {
		certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load etcd client key pair: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return tlsConfig, nil
}

// Close releases etcd client resources.
func (c *Client) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

func cleanRoot(value string) string { return "/" + strings.Trim(strings.TrimSpace(value), "/") }
