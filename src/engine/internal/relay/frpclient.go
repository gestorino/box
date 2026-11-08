// Package relay is relayd's: the box's connection to the relay, so phones reach it from the
// internet. Design: docs/superpowers/specs/2026-11-08-remote-access-box-design.md.
package relay

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
)

// RequestedSubdomain is the subdomain every box asks the relay for. It is never the box's name
// on the relay: the control plane rewrites it, at NewProxy, to the name it gave the box.
const RequestedSubdomain = "box"

// FrpConfig is what the box's frp client needs.
type FrpConfig struct {
	// Server is the relay's address, host:port (relay.<box-domain>:7000).
	Server string
	// CAFile holds the PEM roots the relay's certificate must chain to. Required: without one
	// frp doesn't verify the relay (pkg/transport/tls.go, NewClientTLSConfig), and the box's
	// secret would go to whoever answers.
	CAFile string
	// ServerName is the name the relay's certificate must carry (relay.<box-domain>).
	ServerName string
	// Metas reach the control plane at every hook: box_id and secret.
	Metas map[string]string
	// ProxyName names the box's one proxy. frps keeps proxy names across all its clients and
	// refuses a second of one name, so it is the box's own.
	ProxyName string
	// LocalAddr is where the phones' TLS goes, host:port, passed through undecrypted.
	LocalAddr string
}

// NewFrpClient builds the box's frp client: TLS to the relay, verified against CAFile under
// ServerName, with Metas, and one https proxy to LocalAddr. Its Run logs in, and logs in again
// whenever the relay is lost, until its context ends.
func NewFrpClient(c FrpConfig) (*client.Service, error) {
	if c.CAFile == "" {
		return nil, errors.New("relay: a CA file is required: without one frp doesn't verify the relay")
	}
	if c.ServerName == "" || c.ProxyName == "" {
		return nil, errors.New("relay: a server name and a proxy name are required")
	}
	host, port, err := splitAddr(c.Server)
	if err != nil {
		return nil, fmt.Errorf("relay: server %q: %w", c.Server, err)
	}
	localHost, localPort, err := splitAddr(c.LocalAddr)
	if err != nil {
		return nil, fmt.Errorf("relay: local address %q: %w", c.LocalAddr, err)
	}

	common := &v1.ClientCommonConfig{
		ServerAddr:    host,
		ServerPort:    port,
		LoginFailExit: new(false),
		Metadatas:     c.Metas,
	}
	common.Transport.TLS.Enable = new(true)
	common.Transport.TLS.TrustedCaFile = c.CAFile
	common.Transport.TLS.ServerName = c.ServerName

	https := &v1.HTTPSProxyConfig{
		ProxyBaseConfig: v1.ProxyBaseConfig{
			Name:         c.ProxyName,
			Type:         string(v1.ProxyTypeHTTPS),
			ProxyBackend: v1.ProxyBackend{LocalIP: localHost, LocalPort: localPort},
		},
		DomainConfig: v1.DomainConfig{SubDomain: RequestedSubdomain},
	}

	// As frp's cmd/frpc builds one (cmd/frpc/sub/root.go): the proxies in a config source, behind
	// an aggregator.
	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll([]v1.ProxyConfigurer{https}, nil); err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}
	return client.NewService(client.ServiceOptions{
		Common:                 common,
		ConfigSourceAggregator: source.NewAggregator(configSource),
	})
}

func splitAddr(addr string) (string, int, error) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}
