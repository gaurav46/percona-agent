/*
   Copyright (c) 2014-2015, Percona LLC and/or its affiliates. All rights reserved.

   This program is free software: you can redistribute it and/or modify
   it under the terms of the GNU Affero General Public License as published by
   the Free Software Foundation, either version 3 of the License, or
   (at your option) any later version.

   This program is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU Affero General Public License for more details.

   You should have received a copy of the GNU Affero General Public License
   along with this program.  If not, see <http://www.gnu.org/licenses/>
*/

package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/pct"
)

type Repo struct {
	logger    *pct.Logger
	configDir string
	api       pct.APIConnector
	// --
	it  map[string]proto.InstanceConfig
	mux *sync.RWMutex
}

func NewRepo(logger *pct.Logger, configDir string, api pct.APIConnector) *Repo {
	m := &Repo{
		logger:    logger,
		configDir: configDir,
		api:       api,
		// --
		it:  make(map[string]proto.InstanceConfig),
		mux: &sync.RWMutex{},
	}
	return m
}

func (r *Repo) Init() error {
	return r.loadInstances()
}

func (r *Repo) loadInstances() error {
	files, err := filepath.Glob(r.configDir + "/instance-*.conf")
	if err != nil {
		return err
	}

	for _, file := range files {
		r.logger.Debug("Reading " + file)

		part := strings.Split(strings.TrimSuffix(filepath.Base(file), ".conf"), "-")
		if len(part) != 2 {
			return errors.New("Invalid instance file name: " + file)
		}
		id := part[1]
		if !valid(id) {
			return fmt.Errorf("Invalid instance file name: %s", file)
		}

		data, err := ioutil.ReadFile(file)
		if err != nil {
			return fmt.Errorf("%s: %v", file, err)
		}

		var it *proto.InstanceConfig
		if err := json.Unmarshal(data, &it); err != nil {
			return fmt.Errorf("Could not unmarshal file %s: %v", file, err)
		}

		if err := r.Add(*it, false); err != nil {
			return fmt.Errorf("%s: %v", file, err)
		}

		r.logger.Info("Loaded " + file)
	}
	return nil
}

func (r *Repo) Add(it proto.InstanceConfig, writeToDisk bool) error {
	r.logger.Debug("Add:call")
	defer r.logger.Debug("Add:return")

	r.mux.Lock()
	defer r.mux.Unlock()

	return r.add(it, writeToDisk)
}

func (r *Repo) add(it proto.InstanceConfig, writeToDisk bool) error {
	r.logger.Debug("add:call")
	defer r.logger.Debug("add:return")

	if _, ok := r.it[it.UUID]; ok {
		return pct.DuplicateInstanceError{Id: it.UUID}
	}

	if writeToDisk {
		if err := pct.Basedir.WriteConfig(r.configName(it.UUID), it); err != nil {
			return err
		}
		r.logger.Info("Added " + it.UUID)
	}

	r.it[it.UUID] = it
	return nil
}

func (r *Repo) Get(id string) (it *proto.InstanceConfig, err error) {
	r.logger.Debug("Get:call")
	defer r.logger.Debug("Get:return")

	r.mux.Lock()
	defer r.mux.Unlock()

	return r.get(id)
}

func (r *Repo) get(id string) (it *proto.InstanceConfig, err error) {
	r.logger.Debug("get:call")
	defer r.logger.Debug("get:return")

	if !valid(id) {
		return nil, pct.InvalidInstanceError{Id: id}
	}

	// Get instance info locally, from file on disk.
	inst, ok := r.it[id]

	if !ok {
		// Get instance info from API.
		link := r.api.EntryLink("insts")
		if link == "" {
			r.logger.Warn("No 'insts' API link")
			return nil, pct.UnknownInstanceError{Id: id}
		}
		url := fmt.Sprintf("%s/%s", link, id)
		r.logger.Info("GET", url)
		code, data, err := r.api.Get(r.api.ApiKey(), url)
		if err != nil {
			return nil, fmt.Errorf("Failed to get %s instance from %s: %s", id, link, err)
		} else if code != 200 {
			return nil, fmt.Errorf("Getting %s instance from %s returned code %d, expected 200", id, link, code)
		} else if data == nil {
			return nil, fmt.Errorf("Getting %s instance from %s did not return data")
		} else {
			var it *proto.InstanceConfig
			if err := json.Unmarshal(data, &it); err != nil {
				return nil, fmt.Errorf("Failed to unmarshal instance data provided by API: %s", err)
			}
			// Save new instance locally.
			if err := r.add(*it, true); err != nil {
				return nil, fmt.Errorf("Failed to add new instance: %s", err)
			}
			// Recurse to re-get and return new instance.
			return r.get(id)
		}
	}

	return &inst, nil
}

func (r *Repo) Remove(id string) error {
	r.logger.Debug("Remove:call")
	defer r.logger.Debug("Remove:return")

	// todo: API --> agent --> side.Remove()
	// Agent should stop all services using the instance before call this.
	if !valid(id) {
		return pct.InvalidInstanceError{Id: id}
	}

	r.mux.Lock()
	defer r.mux.Unlock()

	if _, ok := r.it[id]; !ok {
		return pct.UnknownInstanceError{Id: id}
	}

	file := r.configDir + "/instance-" + id + ".conf"
	r.logger.Info("Removing", file)
	if err := os.Remove(file); err != nil {
		return err
	}

	delete(r.it, id)
	r.logger.Info("Removed " + id)
	return nil
}

func valid(id string) bool {
	validRE, _ := regexp.Compile("^[[:xdigit:]]{32}$")
	if !validRE.MatchString(id) {
		return false
	}
	return true
}

func (r *Repo) configName(id string) string {
	return fmt.Sprintf("instance-%s", id)
}

func (r *Repo) List() []proto.InstanceConfig {
	r.mux.Lock()
	defer r.mux.Unlock()
	instances := make([]proto.InstanceConfig, 0)
	for _, inst := range r.it {
		instances = append(instances, inst)
	}
	return instances
}

func isMySQLConfig(inst *proto.InstanceConfig) bool {
	if inst.Type == "MySQL" && inst.Prefix == "mysql" {
		return true
	}
	return false
}

func isOSConfig(inst *proto.InstanceConfig) bool {
	if inst.Type == "OS" && inst.Prefix == "os" {
		return true
	}
	return false
}
