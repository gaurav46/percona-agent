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

package agent

const (
	DEFAULT_API_HOSTNAME = "cloud-api.percona.com"
	DEFAULT_KEEPALIVE    = 76
	DEFAULT_PIDFILE      = "percona-agent.pid"
)

type Config struct {
	AgentUuid   string
	ApiHostname string
	ApiKey      string
	Keepalive   uint
	Links       map[string]string `json:",omitempty"`
	PidFile     string
}
