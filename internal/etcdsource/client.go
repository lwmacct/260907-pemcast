// Package etcdsource reads immutable pemcast v6 bundles and active pointers from etcd.
package etcdsource

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
)

// ProtocolRoot is the fixed v6 protocol root below the configured etcd prefix.
const ProtocolRoot = keyspace.ProtocolRoot

// DefaultPrefix is the default configurable etcd namespace prefix.
const DefaultPrefix = keyspace.DefaultPrefix

// Client wraps the official etcd v3 client with pemcast protocol paths.
type Client struct {
	client         *clientv3.Client
	requestTimeout time.Duration
	keys           keyspace.Keys
}

// New creates an etcd client. It does not require the cluster to be reachable yet.
func New(cfg config.Etcd) (*Client, error) {
	keys, err := keyspace.NewKeys(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := buildTLSConfig(cfg.TLS)
	if err != nil {
		return nil, err
	}
	username, password, err := config.SplitEtcdUser(cfg.User)
	if err != nil {
		return nil, err
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   append([]string(nil), cfg.Endpoints...),
		Username:    username,
		Password:    password,
		DialTimeout: cfg.DialTimeout,
		TLS:         tlsConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("create etcd client: %w", err)
	}
	return &Client{client: client, requestTimeout: cfg.RequestTimeout, keys: keys}, nil
}

func newClient(client *clientv3.Client, requestTimeout time.Duration) *Client {
	return &Client{
		client:         client,
		requestTimeout: requestTimeout,
		keys:           keyspace.Default(),
	}
}

// Prefix returns the normalized configured etcd namespace prefix.
func (c *Client) Prefix() string { return c.keys.Prefix() }

// ActiveKey returns one target's active pointer key.
func (c *Client) ActiveKey(targetID string) string { return c.keys.ActiveKey(targetID) }

// BundleKey returns one immutable bundle path.
func (c *Client) BundleKey(targetID, generation string) string {
	return c.keys.BundleKey(targetID, generation)
}

// ActivePrefix returns the contiguous active-pointer prefix.
func (c *Client) ActivePrefix() string {
	return c.keys.ActivePrefix()
}

// BundlePrefix returns the immutable bundle prefix.
func (c *Client) BundlePrefix() string {
	return c.keys.BundlePrefix()
}

// TargetBundlePrefix returns one target's immutable bundle prefix.
func (c *Client) TargetBundlePrefix(targetID string) string {
	return c.keys.TargetBundlePrefix(targetID)
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
