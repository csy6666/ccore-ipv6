package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	probeTCPAddress = "2606:4700:4700::1111"
	probePort       = "443"
	probeURL        = "https://cloudflare.com/cdn-cgi/trace"
	stageTimeout    = 5 * time.Second
)

// Result contains an ephemeral connectivity check for one managed address.
// It is intentionally not persisted in the IPv6 state file.
type Result struct {
	Interface  string    `json:"interface"`
	Prefix     string    `json:"prefix"`
	Source     string    `json:"source"`
	TCP        bool      `json:"tcp_ok"`
	HTTP       bool      `json:"http_ok"`
	HTTPStatus int       `json:"http_status,omitempty"`
	LatencyMS  int64     `json:"latency_ms"`
	CheckedAt  time.Time `json:"checked_at"`
	Error      string    `json:"error,omitempty"`
}

// Run checks a public IPv6 TCP destination and then an HTTPS endpoint while
// binding both connections to source. A TCP success with an HTTP failure is
// reported separately so routing and application-layer failures are visible.
func Run(ctx context.Context, interfaceName, prefix string, source netip.Addr) Result {
	result := Result{
		Interface: interfaceName,
		Prefix:    prefix,
		Source:    source.String(),
		CheckedAt: time.Now().UTC(),
	}
	started := time.Now()
	if !source.Is6() || source.Is4In6() {
		result.Error = "source is not a valid IPv6 address"
		return result
	}

	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IP(source.AsSlice())}}
	tcpCtx, cancel := context.WithTimeout(ctx, stageTimeout)
	conn, err := dialer.DialContext(tcpCtx, "tcp6", net.JoinHostPort(probeTCPAddress, probePort))
	cancel()
	if err != nil {
		result.LatencyMS = time.Since(started).Milliseconds()
		result.Error = "TCP 连接失败: " + compactError(err)
		return result
	}
	result.TCP = true
	_ = conn.Close()

	transport := &http.Transport{
		ForceAttemptHTTP2: false,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(dialCtx context.Context, _, address string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(address)
			if splitErr != nil {
				return nil, splitErr
			}
			return dialIPv6(dialCtx, dialer, host, port)
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   stageTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	httpCtx, cancelHTTP := context.WithTimeout(ctx, stageTimeout)
	req, reqErr := http.NewRequestWithContext(httpCtx, http.MethodGet, probeURL, nil)
	if reqErr == nil {
		req.Header.Set("User-Agent", "ccore-ipv6-probe/1")
		response, doErr := client.Do(req)
		if doErr != nil {
			result.Error = "HTTPS 请求失败: " + compactError(doErr)
		} else {
			result.HTTPStatus = response.StatusCode
			_, _ = io.CopyN(io.Discard, response.Body, 4096)
			_ = response.Body.Close()
			result.HTTP = response.StatusCode >= 200 && response.StatusCode < 500
			if !result.HTTP {
				result.Error = fmt.Sprintf("HTTPS 返回 HTTP %d", response.StatusCode)
			}
		}
	} else {
		result.Error = "HTTPS 请求创建失败: " + compactError(reqErr)
	}
	cancelHTTP()
	result.LatencyMS = time.Since(started).Milliseconds()
	return result
}

func dialIPv6(ctx context.Context, dialer *net.Dialer, host, port string) (net.Conn, error) {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return dialer.DialContext(ctx, "tcp6", net.JoinHostPort(ip.String(), port))
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip6", host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, address := range addresses {
		conn, dialErr := dialer.DialContext(ctx, "tcp6", net.JoinHostPort(address.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = errors.New("no IPv6 address returned")
	}
	return nil, lastErr
}

func compactError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}
