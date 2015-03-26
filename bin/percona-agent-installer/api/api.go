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

package api

import (
	"encoding/json"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/percona/cloud-protocol/proto"
	mmMySQL "github.com/percona/percona-agent/mm/mysql"
	mmServer "github.com/percona/percona-agent/mm/system"
	"github.com/percona/percona-agent/pct"
	"github.com/percona/percona-agent/qan"
	sysconfigMySQL "github.com/percona/percona-agent/sysconfig/mysql"
	"log"
	"net/http"
)

type Api struct {
	apiConnector pct.APIConnector
	debug        bool
}

func New(apiConnector pct.APIConnector, debug bool) *Api {
	return &Api{
		apiConnector: apiConnector,
		debug:        debug,
	}
}

func (a *Api) Init(hostname, apiKey string, headers map[string]string) (code int, err error) {
	return a.apiConnector.Init(hostname, apiKey, headers)
}

func (a *Api) CreateServerInstance(si *proto.ServerInstance) (*proto.ServerInstance, error) {
	// POST <api>/instances/server
	data, err := json.Marshal(si)
	if err != nil {
		return nil, err
	}
	url := a.apiConnector.URL("instances", "server")
	resp, _, err := a.apiConnector.Post(a.apiConnector.ApiKey(), url, data)
	if a.debug {
		log.Printf("resp=%#v\n", resp)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}
	// Create new instance, if it already exist then just use it
	// todo: better handling of duplicate instance
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		return nil, fmt.Errorf("Failed to create server instance (status code %d)", resp.StatusCode)
	}

	// API returns URI of new resource in Location header
	uri := resp.Header.Get("Location")
	if uri == "" {
		return nil, fmt.Errorf("API did not return location of new server instance")
	}

	// GET <api>/instances/server/id (URI)
	code, data, err := a.apiConnector.Get(a.apiConnector.ApiKey(), uri)
	if a.debug {
		log.Printf("code=%d\n", code)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Failed to get new server instance (status code %d)", code)
	}
	if err := json.Unmarshal(data, si); err != nil {
		return nil, fmt.Errorf("Failed to parse server instance entity: %s", err)
	}
	return si, nil
}

func (a *Api) CreateMySQLInstance(mi *proto.MySQLInstance) (*proto.MySQLInstance, error) {
	// POST <api>/instances/mysql
	data, err := json.Marshal(mi)
	if err != nil {
		return nil, err
	}
	url := a.apiConnector.URL("instances", "mysql")
	resp, _, err := a.apiConnector.Post(a.apiConnector.ApiKey(), url, data)
	if a.debug {
		log.Printf("resp=%#v\n", resp)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}

	// Create new instance, if it already exist then update it
	if resp.StatusCode == http.StatusConflict {
		// API returns URI of existing resource in Location header
		uri := resp.Header.Get("Location")
		if uri == "" {
			return nil, fmt.Errorf("API did not return location of existing MySQL instance")
		}

		resp, _, err := a.apiConnector.Put(a.apiConnector.ApiKey(), uri, data)
		if a.debug {
			log.Printf("resp=%#v\n", resp)
			log.Printf("err=%s\n", err)
		}
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Failed to update MySQL instance (status code %d)", resp.StatusCode)
		}
	} else if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("Failed to create MySQL instance (status code %d)", resp.StatusCode)
	}

	// API returns URI of new (or already existing one) resource in Location header
	uri := resp.Header.Get("Location")
	if uri == "" {
		return nil, fmt.Errorf("API did not return location of new MySQL instance")
	}

	// GET <api>/instances/mysql/id (URI)
	code, data, err := a.apiConnector.Get(a.apiConnector.ApiKey(), uri)
	if a.debug {
		log.Printf("code=%d\n", code)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Failed to get new MySQL instance (status code %d)", code)
	}
	if err := json.Unmarshal(data, mi); err != nil {
		return nil, fmt.Errorf("Failed to parse MySQL instance entity: %s", err)
	}
	return mi, nil
}

func (a *Api) CreateAgent(agent *proto.Agent) (*proto.Agent, error) {
	data, err := json.Marshal(agent)
	if err != nil {
		return nil, err
	}
	url := a.apiConnector.URL("agents")
	resp, _, err := a.apiConnector.Post(a.apiConnector.ApiKey(), url, data)
	if a.debug {
		log.Printf("resp=%#v\n", resp)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusConflict {
		// agent was created or already exist - either is ok, continue
	} else if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-Percona-Agents-Limit") != "" {
		return nil, fmt.Errorf(
			"Maximum number of %s agents exceeded.\n"+
				"Go to https://cloud.percona.com/agents and remove unused agents or contact Percona to increase limit.",
			resp.Header.Get("X-Percona-Agents-Limit"),
		)
	} else {
		return nil, fmt.Errorf("Failed to create agent instance (status code %d)", resp.StatusCode)
	}

	// API returns URI of new resource in Location header
	uri := resp.Header.Get("Location")
	if uri == "" {
		return nil, fmt.Errorf("API did not return location of new agent")
	}

	// GET <api>/agents/:uuid
	code, data, err := a.apiConnector.Get(a.apiConnector.ApiKey(), uri)
	if a.debug {
		log.Printf("code=%d\n", code)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Failed to get new agent (status code %d)", code)
	}
	if err := json.Unmarshal(data, agent); err != nil {
		return nil, fmt.Errorf("Failed to parse agent entity: %s", err)
	}
	return agent, nil
}

func (a *Api) UpdateAgent(agent *proto.Agent, uuid string) (*proto.Agent, error) {
	data, err := json.Marshal(agent)
	if err != nil {
		return nil, err
	}
	url := a.apiConnector.URL("agents", uuid)
	resp, _, err := a.apiConnector.Put(a.apiConnector.ApiKey(), url, data)
	if a.debug {
		log.Printf("resp=%#v\n", resp)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Failed to update agent via API (status code %d)", resp.StatusCode)
	}
	return agent, nil
}

func (a *Api) GetMmServerConfig(si *proto.ServerInstance) (*proto.AgentConfig, error) {
	url := a.apiConnector.URL("/configs/mm/default-server")
	code, data, err := a.apiConnector.Get(a.apiConnector.ApiKey(), url)
	if a.debug {
		log.Printf("code=%d\n", code)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Failed to get default server monitor config (%s, status %d)", url, code)
	}
	config := &mmServer.Config{}
	if err := json.Unmarshal(data, config); err != nil {
		return nil, err
	}
	config.Service = "server"
	config.InstanceId = si.Id

	bytes, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	agentConfig := &proto.AgentConfig{
		InternalService: "mm",
		ExternalService: proto.ServiceInstance{
			Service:    "server",
			InstanceId: si.Id,
		},
		Config:  string(bytes),
		Running: true,
	}
	return agentConfig, nil
}

func (a *Api) GetMmMySQLConfig(mi *proto.MySQLInstance) (*proto.AgentConfig, error) {
	url := a.apiConnector.URL("/configs/mm/default-mysql")
	code, data, err := a.apiConnector.Get(a.apiConnector.ApiKey(), url)
	if a.debug {
		log.Printf("code=%d\n", code)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Failed to get default MySQL monitor config (%s, status %d)", url, code)
	}
	config := &mmMySQL.Config{}
	if err := json.Unmarshal(data, config); err != nil {
		return nil, err
	}
	config.Service = "mysql"
	config.InstanceId = mi.Id

	bytes, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	agentConfig := &proto.AgentConfig{
		InternalService: "mm",
		ExternalService: proto.ServiceInstance{
			Service:    "mysql",
			InstanceId: mi.Id,
		},
		Config:  string(bytes),
		Running: true,
	}
	return agentConfig, nil
}

func (a *Api) GetSysconfigMySQLConfig(mi *proto.MySQLInstance) (*proto.AgentConfig, error) {
	url := a.apiConnector.URL("/configs/sysconfig/default-mysql")
	code, data, err := a.apiConnector.Get(a.apiConnector.ApiKey(), url)
	if a.debug {
		log.Printf("code=%d\n", code)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Failed to get default MySQL sysconfig config (%s, status %d)", url, code)
	}
	config := &sysconfigMySQL.Config{}
	if err := json.Unmarshal(data, config); err != nil {
		return nil, err
	}
	config.Service = "mysql"
	config.InstanceId = mi.Id

	bytes, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	agentConfig := &proto.AgentConfig{
		InternalService: "sysconfig",
		ExternalService: proto.ServiceInstance{
			Service:    "mysql",
			InstanceId: mi.Id,
		},
		Config:  string(bytes),
		Running: true,
	}
	return agentConfig, nil
}

func (a *Api) GetQanConfig(mi *proto.MySQLInstance) (*proto.AgentConfig, error) {
	url := a.apiConnector.URL("/configs/qan/default")
	code, data, err := a.apiConnector.Get(a.apiConnector.ApiKey(), url)
	if a.debug {
		log.Printf("code=%d\n", code)
		log.Printf("err=%s\n", err)
	}
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Failed to get default Query Analytics config (%s, status %d)", url, code)
	}
	config := &qan.Config{}
	if err := json.Unmarshal(data, config); err != nil {
		return nil, err
	}
	config.Service = "mysql"
	config.InstanceId = mi.Id

	bytes, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	agentConfig := &proto.AgentConfig{
		InternalService: "qan",
		ExternalService: proto.ServiceInstance{
			Service:    "mysql",
			InstanceId: mi.Id,
		},
		Config:  string(bytes),
		Running: true,
	}
	return agentConfig, nil
}
