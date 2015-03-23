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
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/instance"
	"github.com/percona/percona-agent/mysql"
	"github.com/percona/percona-agent/pct"
	"github.com/percona/percona-agent/test"
	"github.com/percona/percona-agent/test/mock"
	. "gopkg.in/check.v1"
)

// Hook up gocheck into the "go test" runner.
func Test(t *testing.T) { TestingT(t) }

type RepoTestSuite struct {
	tmpDir    string
	logChan   chan *proto.LogEntry
	logger    *pct.Logger
	configDir string
	api       *mock.API
	//instances []proto.InstanceConfig
	repo *instance.Repo
}

var _ = Suite(&RepoTestSuite{})

func (s *RepoTestSuite) SetUpSuite(t *C) {
	var err error
	s.tmpDir, err = ioutil.TempDir("/tmp", "instance-test-")
	t.Assert(err, IsNil)

	if err := pct.Basedir.Init(s.tmpDir); err != nil {
		t.Fatal(err)
	}
	s.configDir = pct.Basedir.Dir("config")

	s.logChan = make(chan *proto.LogEntry, 0)
	s.logger = pct.NewLogger(s.logChan, "pct-repo-test")

	// TODO: Remove this only for devel purposes
	go func() {
		select {
		case log := <-s.logChan:
			fmt.Println(log)
		}
	}()
}

func (s *RepoTestSuite) SetUpTest(t *C) {
	files, _ := filepath.Glob(s.configDir + "/*")
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			t.Error(err)
		}
	}

	links := map[string]string{
		"insts": "http://localhost/insts",
	}
	s.api = mock.NewAPI("http://localhost", "http://localhost", "123", "abc-123-def", links)
	s.repo = instance.NewRepo(s.logger, s.configDir, s.api)
	t.Assert(s.repo, NotNil)

	files, err := filepath.Glob(test.RootDir + "/instance/instance-*.conf")
	t.Assert(err, IsNil)

	for _, file := range files {
		err := test.CopyFile(file, s.configDir)
		t.Assert(err, IsNil)
	}
}

func (s *RepoTestSuite) TearDownSuite(t *C) {
	if err := os.RemoveAll(s.tmpDir); err != nil {
		t.Error(err)
	}
}

// --------------------------------------------------------------------------

func (s *RepoTestSuite) TestInit(t *C) {
	err := s.repo.Init()
	t.Assert(err, IsNil)

	insts := s.repo.List()
	t.Assert(len(insts), Equals, 4)
	instsIDs := make([]string, 0)
	for _, inst := range insts {
		instsIDs = append(instsIDs, inst.UUID)
	}

	expectIDs := []string{"31dd3b7b602849f8871fd3e7acc8c2e3",
		"dc2b15a5400b4c67ab27848255468e65",
		"c540346a644b404a9d2ae006122fc5a2",
		"fb1be804a02ae1d4c6418d72c0dfe84d"}

	sort.Strings(instsIDs)
	sort.Strings(expectIDs)

	if same, diff := test.IsDeeply(instsIDs, expectIDs); !same {
		test.Dump(instsIDs)
		test.Dump(expectIDs)
		t.Error(diff)
	}
}

func (s *RepoTestSuite) TestInitDuplicatedInstance(t *C) {
	// Lets copy and extra config file containing an already present UUID.
	// Filename UUID is valid
	err := test.CopyFile(test.RootDir+"/instance/instance-31dd3b7b602849f8871fd3e7acc8c2e3.conf",
		s.configDir+"/instance-088fd03e46b795858d0dba8f67b3ac6e.conf")
	t.Assert(err, IsNil)

	expect := pct.DuplicateInstanceError{Id: "31dd3b7b602849f8871fd3e7acc8c2e3"}
	err = s.repo.Init()
	t.Assert(err, NotNil)
	if expect.Error() == err.Error() {
		t.Error("Init should have returned pct.DuplicatedInstanceError")
	}
}

func (s *RepoTestSuite) TestGetDownload(t *C) {
	configFile := s.configDir + "/instance-31dd3b7b602849f8871fd3e7acc8c2e3.conf"

	// Remove one config file from configDir
	err := os.Remove(configFile)
	t.Assert(err, IsNil)

	bin, err := ioutil.ReadFile(test.RootDir + "/instance/instance-31dd3b7b602849f8871fd3e7acc8c2e3.conf")
	t.Assert(err, IsNil)
	s.api.GetData = [][]byte{bin}
	s.api.GetCode = []int{http.StatusOK}

	// This should download the config and place it in configDir
	_, err = s.repo.Get("31dd3b7b602849f8871fd3e7acc8c2e3")
	t.Assert(err, IsNil)
	t.Assert(pct.FileExists(configFile), Equals, true)
	downloadedFile, err := ioutil.ReadFile(configFile)
	t.Assert(err, IsNil)
	var downloadedConfig *proto.InstanceConfig
	err = json.Unmarshal(downloadedFile, &downloadedConfig)
	t.Assert(err, IsNil)
	var expectConfig *proto.InstanceConfig
	err = json.Unmarshal(bin, &expectConfig)
	t.Assert(err, IsNil)

	if same, _ := test.IsDeeply(*downloadedConfig, *expectConfig); !same {
		t.Error("Downloaded instances file is different from the original test config file")
	}
}

func (s *RepoTestSuite) TestGetAddRemove(t *C) {
	t.Check(test.FileExists(s.configDir+"/instance-e4b65f107a4caca10e72ac1f1b23e4aa.conf"), Equals, false)

	mysqlIt := proto.InstanceConfig{}
	mysqlIt.Type = "MySQL"
	mysqlIt.Prefix = "mysql"
	mysqlIt.UUID = "e4b65f107a4caca10e72ac1f1b23e4aa"
	mysqlIt.Properties = map[string]string{"dsn": "test:test@localhost/db1"}

	err := s.repo.Add(mysqlIt, true)
	t.Assert(err, IsNil)

	t.Check(test.FileExists(s.configDir+"/instance-e4b65f107a4caca10e72ac1f1b23e4aa.conf"), Equals, true)

	var got *proto.InstanceConfig
	got, err = s.repo.Get("e4b65f107a4caca10e72ac1f1b23e4aa")
	t.Assert(err, IsNil)
	if same, diff := test.IsDeeply(*got, mysqlIt); !same {
		t.Error(diff)
	}

	data, err := ioutil.ReadFile(s.configDir + "/instance-e4b65f107a4caca10e72ac1f1b23e4aa.conf")
	t.Assert(err, IsNil)

	err = json.Unmarshal(data, got)
	t.Assert(err, IsNil)
	if same, diff := test.IsDeeply(*got, mysqlIt); !same {
		t.Error(diff)
	}

	s.repo.Remove("e4b65f107a4caca10e72ac1f1b23e4aa")
	t.Check(test.FileExists(s.configDir+"/instance-e4b65f107a4caca10e72ac1f1b23e4aa.conf"), Equals, false)
}

///////////////////////////////////////////////////////////////////////////////
//// Manager test suite
///////////////////////////////////////////////////////////////////////////////

type ManagerTestSuite struct {
	tmpDir    string
	logChan   chan *proto.LogEntry
	logger    *pct.Logger
	configDir string
	api       *mock.API
}

var _ = Suite(&ManagerTestSuite{})

func (s *ManagerTestSuite) SetUpSuite(t *C) {
	var err error
	s.tmpDir, err = ioutil.TempDir("/tmp", "agent-test")
	t.Assert(err, IsNil)

	if err := pct.Basedir.Init(s.tmpDir); err != nil {
		t.Fatal(err)
	}
	s.configDir = pct.Basedir.Dir("config")

	s.logChan = make(chan *proto.LogEntry, 10)
	s.logger = pct.NewLogger(s.logChan, "pct-it-test")

	links := map[string]string{
		"agent": "http://localhost/agent",
		"insts": "http://localhost/insts",
	}
	s.api = mock.NewAPI("http://localhost", "http://localhost", "123", "abc-123-def", links)
}

func (s *ManagerTestSuite) SetUpTest(t *C) {
	files, _ := filepath.Glob(s.configDir + "/*")
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			t.Error(err)
		}
	}
}

func (s *ManagerTestSuite) TearDownSuite(t *C) {
	if err := os.RemoveAll(s.tmpDir); err != nil {
		t.Error(err)
	}
}

var dsn = os.Getenv("PCT_TEST_MYSQL_DSN")

//// --------------------------------------------------------------------------

func (s *ManagerTestSuite) TestHandleGetInfoMySQL(t *C) {
	if dsn == "" {
		t.Fatal("PCT_TEST_MYSQL_DSN is not set")
	}

	/**
	 * First get MySQL info manually.  This is what GetInfo should do, too.
	 */

	conn := mysql.NewConnection(dsn)
	if err := conn.Connect(1); err != nil {
		t.Fatal(err)
	}
	var hostname, distro, version string
	sql := "SELECT" +
		" CONCAT_WS('.', @@hostname, IF(@@port='3306',NULL,@@port)) AS Hostname," +
		" @@version_comment AS Distro," +
		" @@version AS Version"
	if err := conn.DB().QueryRow(sql).Scan(&hostname, &distro, &version); err != nil {
		t.Fatal(err)
	}

	/**
	 * Now use the instance manager and GetInfo to get MySQL info like API would.
	 */

	// Create an instance manager.
	mrm := mock.NewMrmsMonitor()
	m := instance.NewManager(s.logger, s.configDir, s.api, mrm)
	t.Assert(m, NotNil)

	err := m.Start()
	t.Assert(err, IsNil)

	// API should send Cmd[Service:"instance", Cmd:"GetInfo",
	//                     Data:proto.InstanceConfig[]]
	// Only DSN is needed.  We set Id just to test that it's not changed.
	mysqlIt := proto.InstanceConfig{}
	mysqlIt.Type = "MySQL"
	mysqlIt.Prefix = "mysql"
	mysqlIt.UUID = "e4b65f107a4caca10e72ac1f1b23e4aa"
	mysqlIt.Properties = map[string]string{"dsn": dsn}

	mysqlData, err := json.Marshal(mysqlIt)
	t.Assert(err, IsNil)

	cmd := &proto.Cmd{
		Cmd:  "GetInfo",
		Data: mysqlData,
	}

	reply := m.Handle(cmd)

	fmt.Println(reply)
	var got *proto.InstanceConfig
	err = json.Unmarshal(reply.Data, &got)
	t.Assert(err, IsNil)

	t.Check(got.UUID, Equals, "e4b65f107a4caca10e72ac1f1b23e4aa")     // not changed
	t.Check(got.Properties["dsn"], Equals, mysqlIt.Properties["dsn"]) // not changed
	t.Check(got.Properties["hostname"], Equals, hostname)             // new
	t.Check(got.Properties["distro"], Equals, distro)                 // new
	t.Check(got.Properties["version"], Equals, version)               // new
}

func (s *ManagerTestSuite) TestHandleAdd(t *C) {
	// Create an instance manager.
	mrm := mock.NewMrmsMonitor()
	m := instance.NewManager(s.logger, s.configDir, s.api, mrm)
	t.Assert(m, NotNil)

	mysqlIt := proto.InstanceConfig{}
	mysqlIt.Type = "MySQL"
	mysqlIt.Prefix = "mysql"
	mysqlIt.UUID = "e4b65f107a4caca10e72ac1f1b23e4aa"
	mysqlIt.Properties = map[string]string{"dsn": dsn}

	mysqlData, err := json.Marshal(mysqlIt)
	t.Assert(err, IsNil)

	cmd := &proto.Cmd{
		Cmd:  "Add",
		Data: mysqlData,
	}

	reply := m.Handle(cmd)
	t.Assert(reply.Error, Equals, "")

	// Test GetMySQLInstances here beacause we already have a Repo with instances
	is := m.GetMySQLInstances()
	t.Assert(is, NotNil)
	t.Assert(len(is), Equals, 1)
	t.Assert(is[0].UUID, Equals, mysqlIt.UUID)
}
