package upstreamprobe

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"docklane.local/docklane/internal/domain"
)

const requestTimeout = 5 * time.Second

type Client struct {
	http *http.Client
}

func NewClient(socketPath string) *Client {
	return &Client{http: &http.Client{
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
					"unix",
					socketPath,
				)
			},
		},
	}}
}

func (client *Client) Probe(
	ctx context.Context,
	upstreamURL string,
) (domain.UpstreamProbe, error) {
	endpoint := "http://unix/v1/probe?" + url.Values{
		"url": {upstreamURL},
	}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return domain.UpstreamProbe{}, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return domain.UpstreamProbe{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&failure)
		if failure.Error != "" {
			return domain.UpstreamProbe{}, errors.New(failure.Error)
		}
		return domain.UpstreamProbe{}, fmt.Errorf(
			"upstream probe returned %s",
			response.Status,
		)
	}
	var result domain.UpstreamProbe
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return domain.UpstreamProbe{}, err
	}
	return result, nil
}

func (client *Client) Check(ctx context.Context) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://unix/health",
		nil,
	)
	if err != nil {
		return err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("upstream probe health returned %s", response.Status)
	}
	return nil
}

func Serve(ctx context.Context, socketPath string) error {
	return serve(ctx, socketPath, probeUpstream)
}

type probeFunc func(context.Context, string) (int, error)

func serve(ctx context.Context, socketPath string, probe probeFunc) error {
	if strings.TrimSpace(socketPath) == "" {
		return errors.New("probe socket path is required")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return fmt.Errorf("create probe socket directory: %w", err)
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("probe socket path exists and is not a socket")
		}
		if err := os.Remove(socketPath); err != nil {
			return fmt.Errorf("remove stale probe socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on probe socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return fmt.Errorf("set probe socket permissions: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/probe", func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		handleProbe(response, request, probe)
	})
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func handleProbe(
	response http.ResponseWriter,
	request *http.Request,
	probe probeFunc,
) {
	upstreamURL := request.URL.Query().Get("url")
	if err := validateURL(upstreamURL); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	started := time.Now()
	result := domain.UpstreamProbe{URL: upstreamURL}
	status, err := probe(request.Context(), upstreamURL)
	if err == nil {
		result.Reachable = true
		result.HTTPStatus = status
	}
	result.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Error = err.Error()
	}
	writeJSON(response, http.StatusOK, result)
}

func probeUpstream(ctx context.Context, upstreamURL string) (int, error) {
	probeRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		upstreamURL+"/",
		nil,
	)
	if err != nil {
		return 0, err
	}
	probeClient := &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout: requestTimeout,
			}).DialContext,
			TLSHandshakeTimeout: requestTimeout,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := probeClient.Do(probeRequest)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

func validateURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid upstream URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("upstream URL scheme must be http or https")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("upstream URL must contain only a hostname and port")
	}
	if parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("upstream URL must not contain a path, query, or fragment")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("upstream URL must contain an explicit valid port")
	}
	return nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int, err error) {
	writeJSON(response, status, map[string]string{"error": err.Error()})
}
