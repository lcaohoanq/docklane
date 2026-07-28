package traefikruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"docklane.local/docklane/internal/domain"
)

const requestTimeout = 5 * time.Second

type Config struct {
	BaseURL      string
	DialAddress  string
	Username     string
	PasswordFile string
	CAFile       string
}

type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

type overviewResponse struct {
	Providers []string `json:"providers"`
}

type componentResponse struct {
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	Errors       []string          `json:"error"`
	ServerStatus map[string]string `json:"serverStatus"`
}

func New(config Config) (*Client, error) {
	baseURL := strings.TrimRight(config.BaseURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, errors.New("Traefik API URL must be a valid HTTPS URL")
	}
	if strings.TrimSpace(config.DialAddress) == "" {
		return nil, errors.New("Traefik API dial address is required")
	}
	if _, _, err := net.SplitHostPort(config.DialAddress); err != nil {
		return nil, fmt.Errorf("invalid Traefik API dial address: %w", err)
	}
	if strings.TrimSpace(config.Username) == "" {
		return nil, errors.New("Traefik API username is required")
	}
	password, err := os.ReadFile(config.PasswordFile)
	if err != nil {
		return nil, fmt.Errorf("read Traefik API password: %w", err)
	}
	passwordValue := strings.TrimRight(string(password), "\r\n")
	if passwordValue == "" {
		return nil, errors.New("Traefik API password is empty")
	}
	certificate, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read Traefik API CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		return nil, errors.New("Traefik API CA contains no certificates")
	}
	dialAddress := config.DialAddress
	return &Client{
		baseURL:  baseURL,
		username: config.Username,
		password: passwordValue,
		http: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				Proxy: nil,
				DialContext: func(
					ctx context.Context,
					_ string,
					_ string,
				) (net.Conn, error) {
					return (&net.Dialer{Timeout: requestTimeout}).DialContext(
						ctx,
						"tcp",
						dialAddress,
					)
				},
				TLSHandshakeTimeout: requestTimeout,
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					RootCAs:    roots,
					ServerName: parsed.Hostname(),
				},
			},
		},
	}, nil
}

func (client *Client) InspectRoute(
	ctx context.Context,
	routeName string,
) (domain.TraefikRouteRuntime, error) {
	var overview overviewResponse
	if _, err := client.get(ctx, "/api/overview", &overview); err != nil {
		return domain.TraefikRouteRuntime{}, err
	}
	result := domain.TraefikRouteRuntime{
		Providers: overview.Providers,
		Router: domain.TraefikRuntimeComponent{
			Name: routeName + "@http",
		},
		Service: domain.TraefikRuntimeComponent{
			Name: routeName + "@http",
		},
	}
	var router componentResponse
	present, err := client.get(
		ctx,
		"/api/http/routers/"+url.PathEscape(result.Router.Name),
		&router,
	)
	if err != nil {
		return domain.TraefikRouteRuntime{}, err
	}
	if present {
		result.Router = component(router)
	}
	var service componentResponse
	present, err = client.get(
		ctx,
		"/api/http/services/"+url.PathEscape(result.Service.Name),
		&service,
	)
	if err != nil {
		return domain.TraefikRouteRuntime{}, err
	}
	if present {
		result.Service = component(service)
		result.ServerStatus = service.ServerStatus
	}
	return result, nil
}

func component(value componentResponse) domain.TraefikRuntimeComponent {
	return domain.TraefikRuntimeComponent{
		Name:    value.Name,
		Present: true,
		Status:  value.Status,
		Errors:  value.Errors,
	}
}

func (client *Client) get(
	ctx context.Context,
	path string,
	output any,
) (bool, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		client.baseURL+path,
		nil,
	)
	if err != nil {
		return false, err
	}
	request.SetBasicAuth(client.username, client.password)
	response, err := client.http.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return false, fmt.Errorf(
			"Traefik API returned %s: %s",
			response.Status,
			strings.TrimSpace(string(body)),
		)
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return false, err
	}
	return true, nil
}
