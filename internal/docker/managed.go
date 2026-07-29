package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	InstallManagedLabel = "com.docklane.managed"
	InstallRoleLabel    = "com.docklane.role"
	InstallSchemaLabel  = "com.docklane.schema"
	InstallIDLabel      = "com.docklane.installation"
	InstallSchemaV1     = "1"
)

var ErrContainerNotFound = errors.New("Docker container not found")

type ManagedNetworkRequest struct {
	Name       string
	Driver     string
	Internal   bool
	Attachable bool
	Labels     map[string]string
}

type ManagedVolumeRequest struct {
	Name   string
	Driver string
	Labels map[string]string
}

type ManagedMountRequest struct {
	Type        string
	Source      string
	Destination string
	ReadOnly    bool
}

type ManagedContainerRequest struct {
	Name                string
	Image               string
	Command             []string
	Networks            []string
	Mounts              []ManagedMountRequest
	Ports               []ContainerPortBinding
	ReadOnlyRootFS      bool
	NoNewPrivileges     bool
	DropAllCapabilities bool
	RestartPolicy       string
	Labels              map[string]string
}

type ManagedContainerState struct {
	ID       string
	Name     string
	Image    string
	Networks []string
	Labels   map[string]string
	Runtime  ContainerRuntime
}

type ManagedLifecycle interface {
	InspectNetwork(context.Context, string) (Network, error)
	CreateManagedNetwork(context.Context, ManagedNetworkRequest) (Network, error)
	RemoveManagedNetwork(context.Context, string) error
	InspectVolume(context.Context, string) (Volume, error)
	CreateManagedVolume(context.Context, ManagedVolumeRequest) (Volume, error)
	RemoveManagedVolume(context.Context, string) error
	InspectManagedContainer(context.Context, string) (ManagedContainerState, error)
	CreateManagedContainer(
		context.Context,
		ManagedContainerRequest,
	) (ManagedContainerState, error)
	StartManagedContainer(context.Context, string) error
	RemoveManagedContainer(context.Context, string) error
}

func (c *Client) CreateManagedNetwork(
	ctx context.Context,
	specification ManagedNetworkRequest,
) (Network, error) {
	payload, err := json.Marshal(map[string]any{
		"Name":           specification.Name,
		"Driver":         specification.Driver,
		"Internal":       specification.Internal,
		"Attachable":     specification.Attachable,
		"CheckDuplicate": true,
		"Labels":         specification.Labels,
	})
	if err != nil {
		return Network{}, err
	}
	response, err := c.managedRequest(
		ctx,
		http.MethodPost,
		"/networks/create",
		payload,
	)
	if err != nil {
		return Network{}, fmt.Errorf(
			"create managed Docker network %s: %w",
			specification.Name,
			err,
		)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return Network{}, dockerResponseError(
			"create managed Docker network "+specification.Name,
			response,
		)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		return Network{}, err
	}
	if created.ID == "" {
		return Network{}, errors.New("Docker returned an empty network ID")
	}
	return c.InspectNetwork(ctx, created.ID)
}

func (c *Client) RemoveManagedNetwork(
	ctx context.Context,
	id string,
) error {
	response, err := c.managedRequest(
		ctx,
		http.MethodDelete,
		"/networks/"+url.PathEscape(id),
		nil,
	)
	if err != nil {
		return fmt.Errorf("remove managed Docker network %s: %w", id, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent ||
		response.StatusCode == http.StatusNotFound {
		return nil
	}
	return dockerResponseError("remove managed Docker network "+id, response)
}

func (c *Client) CreateManagedVolume(
	ctx context.Context,
	specification ManagedVolumeRequest,
) (Volume, error) {
	payload, err := json.Marshal(map[string]any{
		"Name":   specification.Name,
		"Driver": specification.Driver,
		"Labels": specification.Labels,
	})
	if err != nil {
		return Volume{}, err
	}
	response, err := c.managedRequest(
		ctx,
		http.MethodPost,
		"/volumes/create",
		payload,
	)
	if err != nil {
		return Volume{}, fmt.Errorf(
			"create managed Docker volume %s: %w",
			specification.Name,
			err,
		)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return Volume{}, dockerResponseError(
			"create managed Docker volume "+specification.Name,
			response,
		)
	}
	return c.InspectVolume(ctx, specification.Name)
}

func (c *Client) RemoveManagedVolume(
	ctx context.Context,
	name string,
) error {
	response, err := c.managedRequest(
		ctx,
		http.MethodDelete,
		"/volumes/"+url.PathEscape(name),
		nil,
	)
	if err != nil {
		return fmt.Errorf("remove managed Docker volume %s: %w", name, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent ||
		response.StatusCode == http.StatusNotFound {
		return nil
	}
	return dockerResponseError("remove managed Docker volume "+name, response)
}

func (c *Client) InspectManagedContainer(
	ctx context.Context,
	nameOrID string,
) (ManagedContainerState, error) {
	response, err := c.managedRequest(
		ctx,
		http.MethodGet,
		"/containers/"+url.PathEscape(nameOrID)+"/json",
		nil,
	)
	if err != nil {
		return ManagedContainerState{}, fmt.Errorf(
			"inspect managed Docker container %s: %w",
			nameOrID,
			err,
		)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ManagedContainerState{}, fmt.Errorf(
			"%w: %s",
			ErrContainerNotFound,
			nameOrID,
		)
	}
	if response.StatusCode != http.StatusOK {
		return ManagedContainerState{}, dockerResponseError(
			"inspect managed Docker container "+nameOrID,
			response,
		)
	}
	return decodeManagedContainer(response.Body)
}

func (c *Client) CreateManagedContainer(
	ctx context.Context,
	specification ManagedContainerRequest,
) (ManagedContainerState, error) {
	payload, err := managedContainerPayload(specification)
	if err != nil {
		return ManagedContainerState{}, err
	}
	response, err := c.createManagedContainerRequest(ctx, specification.Name, payload)
	if err != nil {
		return ManagedContainerState{}, fmt.Errorf(
			"create managed Docker container %s: %w",
			specification.Name,
			err,
		)
	}
	if response.StatusCode == http.StatusNotFound {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		closeErr := response.Body.Close()
		if readErr != nil {
			return ManagedContainerState{}, errors.Join(readErr, closeErr)
		}
		if !missingImageResponse(body) {
			response.Body = io.NopCloser(bytes.NewReader(body))
			return ManagedContainerState{}, dockerResponseError(
				"create managed Docker container "+specification.Name,
				response,
			)
		}
		if err := c.pullImage(ctx, specification.Image); err != nil {
			return ManagedContainerState{}, fmt.Errorf(
				"acquire image %s for managed Docker container %s: %w",
				specification.Image,
				specification.Name,
				err,
			)
		}
		response, err = c.createManagedContainerRequest(
			ctx,
			specification.Name,
			payload,
		)
		if err != nil {
			return ManagedContainerState{}, fmt.Errorf(
				"create managed Docker container %s after acquiring image: %w",
				specification.Name,
				err,
			)
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return ManagedContainerState{}, dockerResponseError(
			"create managed Docker container "+specification.Name,
			response,
		)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		return ManagedContainerState{}, err
	}
	if created.ID == "" {
		return ManagedContainerState{}, errors.New("Docker returned an empty container ID")
	}
	for _, network := range specification.Networks[1:] {
		aliases := []string{specification.Name}
		if network == "bridge" {
			aliases = nil
		}
		if err := c.ConnectNetwork(
			ctx,
			network,
			created.ID,
			aliases,
		); err != nil {
			return ManagedContainerState{}, errors.Join(
				err,
				c.RemoveManagedContainer(ctx, created.ID),
			)
		}
	}
	return c.InspectManagedContainer(ctx, created.ID)
}

func (c *Client) createManagedContainerRequest(
	ctx context.Context,
	name string,
	payload []byte,
) (*http.Response, error) {
	return c.managedRequest(
		ctx,
		http.MethodPost,
		"/containers/create?name="+url.QueryEscape(name),
		payload,
	)
}

func missingImageResponse(body []byte) bool {
	var payload struct {
		Message string `json:"message"`
	}
	return json.Unmarshal(body, &payload) == nil &&
		strings.Contains(strings.ToLower(payload.Message), "no such image")
}

func (c *Client) pullImage(ctx context.Context, image string) error {
	repository, tag := splitImageReference(image)
	query := url.Values{"fromImage": []string{repository}}
	if tag != "" {
		query.Set("tag", tag)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		"http://docker/images/create?"+query.Encode(),
		nil,
	)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("pull Docker image %s: %w", image, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return dockerResponseError("pull Docker image "+image, response)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return fmt.Errorf("read Docker image pull progress for %s: %w", image, err)
	}
	return nil
}

func splitImageReference(image string) (string, string) {
	if strings.Contains(image, "@") {
		return image, ""
	}
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon > lastSlash {
		return image[:lastColon], image[lastColon+1:]
	}
	return image, ""
}

func (c *Client) StartManagedContainer(
	ctx context.Context,
	id string,
) error {
	response, err := c.managedRequest(
		ctx,
		http.MethodPost,
		"/containers/"+url.PathEscape(id)+"/start",
		nil,
	)
	if err != nil {
		return fmt.Errorf("start managed Docker container %s: %w", id, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent ||
		response.StatusCode == http.StatusNotModified {
		return nil
	}
	return dockerResponseError("start managed Docker container "+id, response)
}

func (c *Client) RemoveManagedContainer(
	ctx context.Context,
	id string,
) error {
	response, err := c.managedRequest(
		ctx,
		http.MethodDelete,
		"/containers/"+url.PathEscape(id)+"?force=true&v=false",
		nil,
	)
	if err != nil {
		return fmt.Errorf("remove managed Docker container %s: %w", id, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent ||
		response.StatusCode == http.StatusNotFound {
		return nil
	}
	return dockerResponseError("remove managed Docker container "+id, response)
}

func (c *Client) managedRequest(
	ctx context.Context,
	method string,
	path string,
	payload []byte,
) (*http.Response, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(
		requestCtx,
		method,
		"http://docker"+path,
		body,
	)
	if err != nil {
		cancel()
		return nil, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &cancelOnClose{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *cancelOnClose) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

func managedContainerPayload(
	specification ManagedContainerRequest,
) ([]byte, error) {
	exposedPorts := map[string]any{}
	portBindings := map[string][]map[string]string{}
	for _, binding := range specification.Ports {
		key := fmt.Sprintf("%d/tcp", binding.ContainerPort)
		exposedPorts[key] = struct{}{}
		portBindings[key] = append(portBindings[key], map[string]string{
			"HostIp":   binding.HostIP,
			"HostPort": fmt.Sprintf("%d", binding.HostPort),
		})
	}
	mounts := make([]map[string]any, 0, len(specification.Mounts))
	for _, mount := range specification.Mounts {
		mounts = append(mounts, map[string]any{
			"Type":     mount.Type,
			"Source":   mount.Source,
			"Target":   mount.Destination,
			"ReadOnly": mount.ReadOnly,
		})
	}
	endpoints := map[string]any{}
	for _, network := range specification.Networks[:min(1, len(specification.Networks))] {
		endpoints[network] = map[string]any{
			"Aliases": []string{specification.Name},
		}
	}
	securityOptions := []string{}
	if specification.NoNewPrivileges {
		securityOptions = append(securityOptions, "no-new-privileges:true")
	}
	capDrop := []string{}
	if specification.DropAllCapabilities {
		capDrop = append(capDrop, "ALL")
	}
	return json.Marshal(map[string]any{
		"Image":        specification.Image,
		"Cmd":          specification.Command,
		"Labels":       specification.Labels,
		"ExposedPorts": exposedPorts,
		"HostConfig": map[string]any{
			"Mounts":         mounts,
			"PortBindings":   portBindings,
			"ReadonlyRootfs": specification.ReadOnlyRootFS,
			"SecurityOpt":    securityOptions,
			"CapDrop":        capDrop,
			"RestartPolicy": map[string]any{
				"Name": specification.RestartPolicy,
			},
		},
		"NetworkingConfig": map[string]any{
			"EndpointsConfig": endpoints,
		},
	})
}

func decodeManagedContainer(reader io.Reader) (ManagedContainerState, error) {
	var payload struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		Image string `json:"Image"`
		State struct {
			Running bool `json:"Running"`
			Health  struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
		Config struct {
			Image  string            `json:"Image"`
			Cmd    []string          `json:"Cmd"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		Mounts []struct {
			Type        string `json:"Type"`
			Name        string `json:"Name"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			RW          bool   `json:"RW"`
		} `json:"Mounts"`
		NetworkSettings struct {
			Networks map[string]json.RawMessage `json:"Networks"`
		} `json:"NetworkSettings"`
		HostConfig struct {
			ReadonlyRootfs bool     `json:"ReadonlyRootfs"`
			Privileged     bool     `json:"Privileged"`
			SecurityOpt    []string `json:"SecurityOpt"`
			CapDrop        []string `json:"CapDrop"`
			RestartPolicy  struct {
				Name string `json:"Name"`
			} `json:"RestartPolicy"`
			PortBindings map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
		} `json:"HostConfig"`
	}
	if err := json.NewDecoder(reader).Decode(&payload); err != nil {
		return ManagedContainerState{}, err
	}
	state := ManagedContainerState{
		ID:       payload.ID,
		Name:     strings.TrimPrefix(payload.Name, "/"),
		Image:    payload.Config.Image,
		Labels:   payload.Config.Labels,
		Networks: make([]string, 0, len(payload.NetworkSettings.Networks)),
		Runtime: ContainerRuntime{
			ImageID:        payload.Image,
			Running:        payload.State.Running,
			Health:         payload.State.Health.Status,
			Command:        append([]string(nil), payload.Config.Cmd...),
			Mounts:         []ContainerMount{},
			PortBindings:   []ContainerPortBinding{},
			ReadOnlyRootFS: payload.HostConfig.ReadonlyRootfs,
			Privileged:     payload.HostConfig.Privileged,
			DroppedCaps:    append([]string(nil), payload.HostConfig.CapDrop...),
			RestartPolicy:  payload.HostConfig.RestartPolicy.Name,
		},
	}
	for network := range payload.NetworkSettings.Networks {
		state.Networks = append(state.Networks, network)
	}
	sort.Strings(state.Networks)
	for _, option := range payload.HostConfig.SecurityOpt {
		if option == "no-new-privileges:true" {
			state.Runtime.NoNewPrivileges = true
		}
	}
	for _, mount := range payload.Mounts {
		source := mount.Source
		if mount.Type == "volume" {
			source = mount.Name
		}
		state.Runtime.Mounts = append(state.Runtime.Mounts, ContainerMount{
			Type:        mount.Type,
			Name:        mount.Name,
			Source:      source,
			Destination: mount.Destination,
			ReadOnly:    !mount.RW,
		})
	}
	for containerPort, bindings := range payload.HostConfig.PortBindings {
		port, err := parseDockerTCPPort(containerPort)
		if err != nil {
			continue
		}
		for _, binding := range bindings {
			hostPort, err := parseUint16(binding.HostPort)
			if err != nil {
				continue
			}
			state.Runtime.PortBindings = append(
				state.Runtime.PortBindings,
				ContainerPortBinding{
					ContainerPort: port,
					HostIP:        binding.HostIP,
					HostPort:      hostPort,
				},
			)
		}
	}
	sort.Slice(state.Runtime.Mounts, func(i, j int) bool {
		return state.Runtime.Mounts[i].Destination <
			state.Runtime.Mounts[j].Destination
	})
	sort.Slice(state.Runtime.PortBindings, func(i, j int) bool {
		left := state.Runtime.PortBindings[i]
		right := state.Runtime.PortBindings[j]
		if left.ContainerPort != right.ContainerPort {
			return left.ContainerPort < right.ContainerPort
		}
		if left.HostIP != right.HostIP {
			return left.HostIP < right.HostIP
		}
		return left.HostPort < right.HostPort
	})
	sort.Strings(state.Runtime.DroppedCaps)
	return state, nil
}
