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

package mock

import (
	"net/http"
)

type API struct {
	origin    string
	hostname  string
	apiKey    string
	agentUuid string
	links     map[string]string
	GetCode   []int
	GetData   [][]byte
	GetError  []error
}

func NewAPI(origin, hostname, apiKey, agentUuid string, links map[string]string) *API {
	a := &API{
		origin:    origin,
		hostname:  hostname,
		apiKey:    apiKey,
		agentUuid: agentUuid,
		links:     links,
	}
	return a
}

func PingAPI(hostname, apiKey string) (bool, *http.Response) {
	return true, nil
}

func (a *API) Init(hostname, apiKey string, headers map[string]string) (code int, err error) {
	return http.StatusOK, nil
}

func (a *API) Connect(hostname, apiKey, agentUuid string) error {
	a.hostname = hostname
	a.apiKey = apiKey
	a.agentUuid = agentUuid
	return nil
}

func (a *API) AgentLink(resource string) string {
	return a.links[resource]
}

func (a *API) EntryLink(resource string) string {
	return a.links[resource]
}

func (a *API) Origin() string {
	return a.origin
}

func (a *API) Hostname() string {
	return a.hostname
}

func (a *API) ApiKey() string {
	return a.apiKey
}

func (a *API) AgentUuid() string {
	return a.agentUuid
}

func (a *API) Get(string, string) (int, []byte, error) {
	code := 200
	var data []byte
	var err error
	if len(a.GetCode) > 0 {
		code = a.GetCode[0]
		a.GetCode = a.GetCode[1:len(a.GetCode)]
	}
	if len(a.GetData) > 0 {
		data = a.GetData[0]
		a.GetData = a.GetData[1:len(a.GetData)]
	}
	if len(a.GetError) > 0 {
		err = a.GetError[0]
		a.GetError = a.GetError[1:len(a.GetError)]
	}
	return code, data, err
}

func (a *API) Post(apiKey, url string, data []byte) (*http.Response, []byte, error) {
	return nil, nil, nil
}

func (a *API) Put(apiKey, url string, data []byte) (*http.Response, []byte, error) {
	return nil, nil, nil
}

func (a *API) URL(paths ...string) string {
	return ""
}
