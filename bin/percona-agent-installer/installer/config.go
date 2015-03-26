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

package installer

import (
	"encoding/json"
	"fmt"
	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/data"
	pctLog "github.com/percona/percona-agent/log"
	"github.com/percona/percona-agent/pct"
)

func (i *Installer) writeInstances(si *proto.ServerInstance, mi *proto.MySQLInstance) error {
	// We could write the instance structs directly, but this is the job of an
	// instance repo and it's easy enough to create one, so do the right thing.
	if si != nil {
		bytes, err := json.Marshal(si)
		if err != nil {
			return err
		}
		if err := i.instanceRepo.Add("server", si.Id, bytes, true); err != nil {
			return err
		}
	}
	if mi != nil {
		bytes, err := json.Marshal(mi)
		if err != nil {
			return err
		}
		if err := i.instanceRepo.Add("mysql", mi.Id, bytes, true); err != nil {
			return err
		}
	}
	return nil
}

func (i *Installer) writeConfigs(configs []proto.AgentConfig) error {
	for _, config := range configs {
		name := config.InternalService
		switch name {
		case "agent", "log", "data", "qan":
		default:
			name += fmt.Sprintf("-%s-%d", config.ExternalService.Service, config.ExternalService.InstanceId)
		}

		if err := pct.Basedir.WriteConfigString(name, config.Config); err != nil {
			return err
		}
	}

	return nil
}

func (i *Installer) getLogConfig() (*proto.AgentConfig, error) {
	config := pctLog.Config{
		File:  pctLog.DEFAULT_LOG_FILE,
		Level: pctLog.DEFAULT_LOG_LEVEL,
	}
	configJson, err := json.Marshal(&config)
	if err != nil {
		return nil, err
	}
	agentConfig := &proto.AgentConfig{
		InternalService: "log",
		Config:          string(configJson),
	}

	return agentConfig, nil
}

func (i *Installer) getDataConfig() (*proto.AgentConfig, error) {
	config := data.Config{
		Encoding:     data.DEFAULT_DATA_ENCODING,
		SendInterval: data.DEFAULT_DATA_SEND_INTERVAL,
	}
	configJson, err := json.Marshal(&config)
	if err != nil {
		return nil, err
	}
	agentConfig := &proto.AgentConfig{
		InternalService: "data",
		Config:          string(configJson),
	}

	return agentConfig, nil
}

func (i *Installer) getAgentConfig() (*proto.AgentConfig, error) {
	configJson, err := json.Marshal(i.agentConfig)
	if err != nil {
		return nil, err
	}
	agentConfig := &proto.AgentConfig{
		InternalService: "agent",
		Config:          string(configJson),
	}

	return agentConfig, nil
}
