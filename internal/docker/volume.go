package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

var ErrVolumeNotFound = errors.New("Docker volume not found")

type Volume struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver"`
	Scope  string            `json:"scope"`
	Labels map[string]string `json:"labels,omitempty"`
}

func (c *Client) InspectVolume(
	ctx context.Context,
	name string,
) (Volume, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		"http://docker/volumes/"+url.PathEscape(name),
		nil,
	)
	if err != nil {
		return Volume{}, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Volume{}, fmt.Errorf("inspect Docker volume %s: %w", name, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Volume{}, ErrVolumeNotFound
	}
	if response.StatusCode != http.StatusOK {
		return Volume{}, dockerResponseError("inspect Docker volume "+name, response)
	}
	var volume Volume
	if err := json.NewDecoder(response.Body).Decode(&volume); err != nil {
		return Volume{}, err
	}
	return volume, nil
}
