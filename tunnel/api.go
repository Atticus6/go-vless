//go:build !notunnel

package tunnel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"github.com/cloudflare/cloudflared/connection"
	"github.com/cloudflare/cloudflared/tunnelrpc/pogs"
	"github.com/google/uuid"
)

const (
	apiURL     = "https://api.trycloudflare.com/tunnel"
	apiTimeout = 30 * time.Second
)

func createTunnel(clientID uuid.UUID) (*connection.TunnelProperties, error) {
	client := &http.Client{
		Transport: &http.Transport{
			TLSHandshakeTimeout:   apiTimeout,
			ResponseHeaderTimeout: apiTimeout,
		},
		Timeout: apiTimeout,
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build tunnel request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cloudflared/embedded-wizzard0-trycloudflared")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request tunnel: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result CreateTunnelResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("tunnel creation failed: %v", result.Errors)
	}

	tunnelID, err := uuid.Parse(result.Result.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse tunnel ID: %w", err)
	}

	return &connection.TunnelProperties{
		Credentials: connection.Credentials{
			AccountTag:   result.Result.AccountTag,
			TunnelSecret: result.Result.Secret,
			TunnelID:     tunnelID,
		},
		QuickTunnelUrl: result.Result.Hostname,
		Client: pogs.ClientInfo{
			ClientID: clientID[:],
			Features: []string{},
			Version:  Version,
			Arch:     runtime.GOOS + "_" + runtime.GOARCH,
		},
	}, nil
}

// CreateTunnelResponse API 响应结构
type CreateTunnelResponse struct {
	Success bool                `json:"success"`
	Result  TunnelCredentials   `json:"result"`
	Errors  []CreateTunnelError `json:"errors"`
}

// CreateTunnelError API 错误结构
type CreateTunnelError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// TunnelCredentials 隧道凭证
type TunnelCredentials struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Hostname   string `json:"hostname"`
	AccountTag string `json:"account_tag"`
	Secret     []byte `json:"secret"`
}
