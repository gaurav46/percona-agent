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

package installer_test

import (
	"fmt"
	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/agent"
	"github.com/percona/percona-agent/bin/percona-agent-installer/api"
	"github.com/percona/percona-agent/bin/percona-agent-installer/installer"
	"github.com/percona/percona-agent/bin/percona-agent-installer/term"
	"github.com/percona/percona-agent/instance"
	"github.com/percona/percona-agent/pct"
	"github.com/percona/percona-agent/test/mock"
	. "gopkg.in/check.v1"
	"os"
	"testing"
)

func Test(t *testing.T) { TestingT(t) }

type InstallerTestSuite struct{}

var _ = Suite(&InstallerTestSuite{})

func (i *InstallerTestSuite) TestIsSupportedMySQLVersion(t *C) {
	var got bool
	var err error

	agentConfig := &agent.Config{}
	flags := installer.Flags{}

	apiConnector := pct.NewAPI()
	api := api.New(apiConnector, false)
	logChan := make(chan *proto.LogEntry, 100)
	logger := pct.NewLogger(logChan, "instance-repo")
	instanceRepo := instance.NewRepo(logger, pct.Basedir.Dir("config"), apiConnector)
	terminal := term.NewTerminal(os.Stdin, false, true)
	inst := installer.NewInstaller(terminal, "", api, instanceRepo, agentConfig, flags)
	conn := mock.NewNullMySQL()
	errSomethingWentWrong := fmt.Errorf("Something went wrong")

	conn.SetAtLeastVersion(false, errSomethingWentWrong)
	got, err = inst.IsVersionSupported(conn)
	t.Assert(conn.Version, Equals, agent.MIN_SUPPORTED_MYSQL_VERSION)
	t.Assert(err, Equals, errSomethingWentWrong)
	t.Assert(got, Equals, false)

	conn.SetAtLeastVersion(true, nil)
	got, err = inst.IsVersionSupported(conn)
	t.Assert(conn.Version, Equals, agent.MIN_SUPPORTED_MYSQL_VERSION)
	t.Assert(err, IsNil)
	t.Assert(got, Equals, true)

	conn.Close()
}
