package security

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type TargetPolicy struct {
	AllowPrivate bool
	Resolver     *net.Resolver
}

func (p TargetPolicy) ValidateURL(ctx context.Context, rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("invalid target URL")
	}
	if parsed.Scheme != "https" && !(p.AllowPrivate && parsed.Scheme == "http") {
		return nil, errors.New("target URL must use HTTPS")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("target URL requires a host and cannot contain credentials")
	}
	if err := p.validateHost(ctx, parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (p TargetPolicy) validateHost(ctx context.Context, host string) error {
	resolver := p.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("target host has no IP addresses")
	}
	for _, address := range addresses {
		if !p.AllowPrivate && blockedAddress(address) {
			return fmt.Errorf("target resolves to blocked address: %s", address)
		}
	}
	return nil
}

func blockedAddress(address netip.Addr) bool {
	return !address.IsValid() || address.IsLoopback() || address.IsPrivate() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsMulticast() || address.IsUnspecified()
}

func (p TargetPolicy) Client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          100,
		IdleConnTimeout:       30 * time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		resolver := p.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		addresses, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve target during dial: %w", err)
		}
		if len(addresses) == 0 {
			return nil, errors.New("target host has no IP addresses during dial")
		}
		for _, candidate := range addresses {
			if !p.AllowPrivate && blockedAddress(candidate) {
				return nil, fmt.Errorf("target resolved to blocked address during dial: %s", candidate)
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		connection, err := transport.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		tlsConnection := tls.Client(connection, &tls.Config{ //nolint:gosec // TLS defaults are secure; ServerName is explicit.
			MinVersion: tls.VersionTLS12,
			ServerName: strings.Trim(host, "[]"),
		})
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			_ = connection.Close()
			return nil, err
		}
		return tlsConnection, nil
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func MaskHeaders(headers http.Header) map[string]string {
	masked := make(map[string]string, len(headers))
	for key := range headers {
		lower := strings.ToLower(key)
		if lower == "authorization" || lower == "cookie" || strings.Contains(lower, "signature") || strings.Contains(lower, "token") || strings.Contains(lower, "key") {
			masked[key] = "[REDACTED]"
		} else {
			masked[key] = strings.Join(headers.Values(key), ",")
		}
	}
	return masked
}
