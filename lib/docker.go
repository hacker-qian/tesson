/*
   Copyright (c) 2016 Andrey Sibiryov <me@kobology.ru>
   Copyright (c) 2016 Other contributors as noted in the AUTHORS file.

   This file is part of Tesson.

   Tesson is free software; you can redistribute it and/or modify
   it under the terms of the GNU Lesser General Public License as published by
   the Free Software Foundation; either version 3 of the License, or
   (at your option) any later version.

   Tesson is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
   GNU Lesser General Public License for more details.

   You should have received a copy of the GNU Lesser General Public License
   along with this program. If not, see <http://www.gnu.org/licenses/>.
*/

package tesson

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/engine-api/client"
	"github.com/docker/engine-api/types"
	"github.com/docker/engine-api/types/container"
	"github.com/docker/go-connections/nat"

	"golang.org/x/net/context"
)

var (
	errGroupDoesNotExist = errors.New("the specified group does not exist")
)

// DockerContext represends a Docker instance client.
type DockerContext interface {
	Exec(group string, opts ExecOptions) error
	List() ([]Group, error)
	Stop(group string, opts StopOptions) error
}

// ExecOptions specifies options for Exec.
type ExecOptions struct {
	Image  string
	Layout []string
	Ports  []string
	Config string
}

// StopOptions specifies options for Stop.
type StopOptions struct {
	Purge bool
}

// Group represents runtime group status.
type Group struct {
	Name   string
	Image  string
	Shards []Shard
}

// Shard represents runtime group member status
type Shard struct {
	Name   string
	ID     string
	Status string
	CPUs   string
}

// Implementation

type docker struct {
	ctx    context.Context
	client *client.Client
}

// NewDockerContext returns a new Docker instance client.
func NewDockerContext(ctx context.Context) (DockerContext, error) {
	r, err := client.NewEnvClient()

	if err != nil {
		return nil, err
	}

	return &docker{ctx: ctx, client: r}, nil
}

func (d *docker) Exec(group string, opts ExecOptions) error {
	c := container.Config{Labels: make(map[string]string)}

	if len(opts.Config) != 0 {
		f, err := os.Open(opts.Config)

		if err != nil {
			return err
		}

		defer f.Close()

		if err := json.NewDecoder(bufio.NewReader(f)).Decode(&c); err != nil {
			return err
		}
	}

	c.Image, c.Labels["tesson.group"] = opts.Image, group

	_, pm, err := nat.ParsePortSpecs(opts.Ports)

	if err != nil {
		return err
	}

	for _, shard := range opts.Layout {
		c.Labels["tesson.shard"] = shard

		if err := d.exec(&c, &container.HostConfig{
			Resources:    container.Resources{CpusetCpus: shard},
			PortBindings: pm,
		}); err != nil {
			return err
		}
	}

	return nil
}

func (d *docker) List() ([]Group, error) {
	list, err := d.client.ContainerList(d.ctx, types.ContainerListOptions{
		All: true,
	})

	if err != nil {
		return nil, err
	}

	m := make(map[string]*Group)

	for _, c := range list {
		var g *Group

		if group, ok := c.Labels["tesson.group"]; !ok {
			continue
		} else if g = m[group]; g == nil {
			g = &Group{Name: group, Image: c.Image}
			m[group] = g
		}

		var name string

		if len(c.Names[0]) == 0 {
			name = "<unknown>"
		} else {
			name = c.Names[0]
		}

		g.Shards = append(g.Shards, Shard{
			Name: name, ID: c.ID, CPUs: c.Labels["tesson.shard"],
			Status: c.Status})
	}

	var r []Group

	for _, v := range m {
		r = append(r, *v)
	}

	return r, nil
}

func (d *docker) Stop(group string, opts StopOptions) error {
	list, err := d.List()

	if err != nil {
		return err
	}

	var index int

	for index = 0; index < len(list); index++ {
		if list[index].Name == group {
			break
		}
	}

	if index >= len(list) {
		return errGroupDoesNotExist
	}

	for _, shard := range list[index].Shards {
		if err := d.stop(shard.ID, opts); err != nil {
			return err
		}
	}

	return nil
}

func (d *docker) exec(c *container.Config, h *container.HostConfig) error {
	r, err := d.client.ContainerCreate(d.ctx, c, h, nil, "")

	if err != nil {
		return err
	}

	log.Infof("instance created: %v", r.ID)

	if err := d.client.ContainerStart(
		d.ctx, r.ID, types.ContainerStartOptions{},
	); err != nil {
		return err
	}

	return nil
}

func (d *docker) stop(id string, opts StopOptions) error {
	r, err := d.client.ContainerInspect(d.ctx, id)

	if err != nil {
		return err
	}

	if r.State.Running {
		if err := d.client.ContainerStop(d.ctx, r.ID, 30); err != nil {
			return err
		}

		log.Infof("instance stopped: %v", r.ID)
	}

	if !opts.Purge {
		return nil
	}

	return d.client.ContainerRemove(d.ctx, r.ID, types.ContainerRemoveOptions{
		RemoveVolumes: true,
	})
}
