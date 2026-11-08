package compute

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const (
	LabelManagedBy = "managed-by"
	LabeUID        = "workspace-uid"
	LabeOwner      = "workspace-owner"
	managedValue   = "docker-workspaces"
	dockerNetwork  = "kind"
)

type DockerProvider struct {
	client  *client.Client
	Publish bool
}

func NewDockerProvider() (*DockerProvider, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}

	return &DockerProvider{
		client:  cli,
		Publish: true,
	}, nil
}

func containerName(uid string) string {
	return fmt.Sprintf("workspace-%s", uid)
}

func (p *DockerProvider) pull(ctx context.Context, image string) error {
	if _, err := p.client.ImageInspect(ctx, image); err == nil {
		return nil
	}
	resp, err := p.client.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer resp.Close()
	return resp.Wait(ctx)
}

func (p *DockerProvider) Get(ctx context.Context, uid string) (*Instance, error) {
	res, err := p.client.ContainerInspect(ctx, containerName(uid), client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil, fmt.Errorf("container for %s: %w", uid, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	c := res.Container

	inst := &Instance{
		ID:   c.ID,
		Name: strings.TrimPrefix(c.Name, "/"),
		UID:  uid,
	}

	if c.State != nil {
		inst.State = string(c.State.Status)
	}

	if t, err := time.Parse(time.RFC3339Nano, c.Created); err == nil {
		inst.CreatedAt = t
	}

	if c.NetworkSettings != nil {
		if ep := c.NetworkSettings.Networks[dockerNetwork]; ep != nil && ep.IPAddress.IsValid() {
			inst.IP = ep.IPAddress.String()
		}

		for _, bindings := range c.NetworkSettings.Ports {
			if len(bindings) > 0 && bindings[0].HostPort != "" {
				inst.URL = "127.0.0.1:" + bindings[0].HostPort
			}
		}
	}
	return inst, nil
}

func (p *DockerProvider) Create(ctx context.Context, req CreateRequest) (*Instance, error) {
	if err := p.pull(ctx, req.Image); err != nil {
		return nil, fmt.Errorf("pull %s: %w", req.Image, err)
	}

	port, err := network.ParsePort(strconv.Itoa(req.Port) + "/tcp")
	if err != nil {
		return nil, err
	}
	hostCfg := &container.HostConfig{NetworkMode: dockerNetwork}

	if p.Publish {
		hostCfg.PortBindings = network.PortMap{
			port: {{HostIP: netip.MustParseAddr("127.0.0.1")}},
		}
	}

	name := containerName(req.UID)
	_, err = p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        req.Image,
			Cmd:          req.Args,
			ExposedPorts: network.PortSet{port: {}},
			Labels: map[string]string{
				LabelManagedBy: managedValue,
				LabeUID:        req.UID,
				LabeOwner:      req.Owner,
			},
		},
		HostConfig: hostCfg,
	})

	if err != nil && !cerrdefs.IsConflict(err) {
		return nil, fmt.Errorf("create container: %w", err)
	}

	if _, err := p.client.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}

	return p.Get(ctx, req.UID)
}

func (p *DockerProvider) Delete(ctx context.Context, uid string) error {
	if _, err := p.client.ContainerRemove(ctx, containerName(uid), client.ContainerRemoveOptions{Force: true}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("container for %s: %w", uid, ErrNotFound)
		}
		return err
	}
	return nil
}

func (p *DockerProvider) List(ctx context.Context) ([]*Instance, error) {
	res, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", LabelManagedBy+"="+managedValue)})
	if err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	out := make([]*Instance, 0, len(res.Items))
	for _, container := range res.Items {
		// Convert each container to an Instance
		inst := &Instance{
			ID:        container.ID,
			UID:       container.Labels[LabeUID],
			State:     string(container.State),
			CreatedAt: time.Unix(container.Created, 0),
		}
		out = append(out, inst)
	}
	return out, nil
}
