/*
   Copyright (c) 2014, Percona LLC and/or its affiliates. All rights reserved.

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

package service_test

import (
	"database/sql"
	"encoding/json"
	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/instance"
	"github.com/percona/percona-agent/mysql"
	"github.com/percona/percona-agent/pct"
	"github.com/percona/percona-agent/query"
	"github.com/percona/percona-agent/query/service"
	"github.com/percona/percona-agent/test/mock"
	"io/ioutil"
	. "launchpad.net/gocheck"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Hook up gocheck into the "go test" runner.
func Test(t *testing.T) { TestingT(t) }

/////////////////////////////////////////////////////////////////////////////
// Manager test suite
/////////////////////////////////////////////////////////////////////////////

type ManagerTestSuite struct {
	logChan       chan *proto.LogEntry
	logger        *pct.Logger
	tickChan      chan time.Time
	readyChan     chan bool
	configDir     string
	tmpDir        string
	dsn           string
	rir           *instance.Repo
	mysqlInstance proto.ServiceInstance
	api           *mock.API
}

var _ = Suite(&ManagerTestSuite{})

func (s *ManagerTestSuite) SetUpSuite(t *C) {
	s.dsn = os.Getenv("PCT_TEST_MYSQL_DSN")
	if s.dsn == "" {
		t.Fatal("PCT_TEST_MYSQL_DSN is not set")
	}

	s.logChan = make(chan *proto.LogEntry, 10)
	s.logger = pct.NewLogger(s.logChan, query.SERVICE_NAME+"-manager-test")

	var err error
	s.tmpDir, err = ioutil.TempDir("/tmp", "agent-test")
	t.Assert(err, IsNil)

	if err := pct.Basedir.Init(s.tmpDir); err != nil {
		t.Fatal(err)
	}
	s.configDir = pct.Basedir.Dir("config")

	// Real instance repo
	s.rir = instance.NewRepo(pct.NewLogger(s.logChan, "im-test"), s.configDir, s.api)
	data, err := json.Marshal(&proto.MySQLInstance{
		Hostname: "db1",
		DSN:      s.dsn,
	})
	t.Assert(err, IsNil)
	s.rir.Add("mysql", 1, data, false)
	s.mysqlInstance = proto.ServiceInstance{Service: "mysql", InstanceId: 1}

	links := map[string]string{
		"agent":     "http://localhost/agent",
		"instances": "http://localhost/instances",
	}
	s.api = mock.NewAPI("http://localhost", "http://localhost", "123", "abc-123-def", links)
}

func (s *ManagerTestSuite) SetUpTest(t *C) {
	glob := filepath.Join(pct.Basedir.Dir("config"), "*")
	files, err := filepath.Glob(glob)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
	}
}

func (s *ManagerTestSuite) TearDownSuite(t *C) {
	if err := os.RemoveAll(s.tmpDir); err != nil {
		t.Error(err)
	}
}

// --------------------------------------------------------------------------

func (s *ManagerTestSuite) TestExplainWithoutDb(t *C) {
	// Create explain service
	explainService := service.NewExplain(s.logger, &mysql.RealConnectionFactory{}, s.rir)

	// Explain query
	query := "SELECT 1"
	expectedExplainResult := &proto.ExplainResult{
		Classic: []*proto.ExplainRow{
			&proto.ExplainRow{
				Id: proto.NullInt64{
					NullInt64: sql.NullInt64{
						Int64: 1,
						Valid: true,
					},
				},
				SelectType: proto.NullString{
					NullString: sql.NullString{
						String: "SIMPLE",
						Valid:  true,
					},
				},
				Table: proto.NullString{
					NullString: sql.NullString{
						String: "",
						Valid:  false,
					},
				},
				CreateTable: proto.NullString{
					NullString: sql.NullString{
						String: "",
						Valid:  false,
					},
				},
				Type: proto.NullString{
					NullString: sql.NullString{
						String: "",
						Valid:  false,
					},
				},
				PossibleKeys: proto.NullString{
					NullString: sql.NullString{
						String: "",
						Valid:  false,
					},
				},
				Key: proto.NullString{
					NullString: sql.NullString{
						String: "",
						Valid:  false,
					},
				},
				KeyLen: proto.NullInt64{
					NullInt64: sql.NullInt64{
						Int64: 0,
						Valid: false,
					},
				},
				Ref: proto.NullString{
					NullString: sql.NullString{
						String: "",
						Valid:  false,
					},
				},
				Rows: proto.NullInt64{
					NullInt64: sql.NullInt64{
						Int64: 0,
						Valid: false,
					},
				},
				Extra: proto.NullString{
					NullString: sql.NullString{
						String: "No tables used",
						Valid:  true,
					},
				},
			},
		},
		JSON: "{\n  \"query_block\": {\n    \"select_id\": 1,\n    \"table\": {\n      \"message\": \"No tables used\"\n    }\n  }\n}",
	}

	explainQuery := &proto.ExplainQuery{
		ServiceInstance: s.mysqlInstance,
		Query:           query,
	}
	data, err := json.Marshal(&explainQuery)
	t.Assert(err, IsNil)

	cmd := &proto.Cmd{
		Service: "query",
		Cmd:     "Explain",
		Data:    data,
	}

	gotReply := explainService.Handle(cmd)
	t.Assert(gotReply, NotNil)
	t.Assert(gotReply.Error, Equals, "")

	var gotExplainResult *proto.ExplainResult
	err = json.Unmarshal(gotReply.Data, &gotExplainResult)
	t.Assert(err, IsNil)
	t.Assert(gotExplainResult, DeepEquals, expectedExplainResult)
}

func (s *ManagerTestSuite) TestExplainWithDb(t *C) {
	// Create explain service
	explainService := service.NewExplain(s.logger, &mysql.RealConnectionFactory{}, s.rir)

	// Explain query
	db := "information_schema"
	query := "SELECT table_name FROM tables WHERE table_name='tables'"

	expectedExplainResult := &proto.ExplainResult{
		Classic: []*proto.ExplainRow{
			&proto.ExplainRow{
				Id: proto.NullInt64{
					NullInt64: sql.NullInt64{
						Int64: 1,
						Valid: true,
					},
				},
				SelectType: proto.NullString{
					NullString: sql.NullString{
						String: "SIMPLE",
						Valid:  true,
					},
				},
				Table: proto.NullString{
					NullString: sql.NullString{
						String: "tables",
						Valid:  true,
					},
				},
				CreateTable: proto.NullString{
					NullString: sql.NullString{
						String: "CREATE TEMPORARY TABLE `TABLES` (\n  `TABLE_CATALOG` varchar(512) NOT NULL DEFAULT '',\n  `TABLE_SCHEMA` varchar(64) NOT NULL DEFAULT '',\n  `TABLE_NAME` varchar(64) NOT NULL DEFAULT '',\n  `TABLE_TYPE` varchar(64) NOT NULL DEFAULT '',\n  `ENGINE` varchar(64) DEFAULT NULL,\n  `VERSION` bigint(21) unsigned DEFAULT NULL,\n  `ROW_FORMAT` varchar(20) DEFAULT NULL,\n  `TABLE_ROWS` bigint(21) unsigned DEFAULT NULL,\n  `AVG_ROW_LENGTH` bigint(21) unsigned DEFAULT NULL,\n  `DATA_LENGTH` bigint(21) unsigned DEFAULT NULL,\n  `MAX_DATA_LENGTH` bigint(21) unsigned DEFAULT NULL,\n  `INDEX_LENGTH` bigint(21) unsigned DEFAULT NULL,\n  `DATA_FREE` bigint(21) unsigned DEFAULT NULL,\n  `AUTO_INCREMENT` bigint(21) unsigned DEFAULT NULL,\n  `CREATE_TIME` datetime DEFAULT NULL,\n  `UPDATE_TIME` datetime DEFAULT NULL,\n  `CHECK_TIME` datetime DEFAULT NULL,\n  `TABLE_COLLATION` varchar(32) DEFAULT NULL,\n  `CHECKSUM` bigint(21) unsigned DEFAULT NULL,\n  `CREATE_OPTIONS` varchar(255) DEFAULT NULL,\n  `TABLE_COMMENT` varchar(2048) NOT NULL DEFAULT ''\n) ENGINE=MEMORY DEFAULT CHARSET=utf8",
						Valid:  true,
					},
				},
				Type: proto.NullString{
					NullString: sql.NullString{
						String: "ALL",
						Valid:  true,
					},
				},
				PossibleKeys: proto.NullString{
					NullString: sql.NullString{
						String: "",
						Valid:  false,
					},
				},
				Key: proto.NullString{
					NullString: sql.NullString{
						String: "TABLE_NAME",
						Valid:  true,
					},
				},
				KeyLen: proto.NullInt64{
					NullInt64: sql.NullInt64{
						Int64: 0,
						Valid: false,
					},
				},
				Ref: proto.NullString{
					NullString: sql.NullString{
						String: "",
						Valid:  false,
					},
				},
				Rows: proto.NullInt64{
					NullInt64: sql.NullInt64{
						Int64: 0,
						Valid: false,
					},
				},
				Extra: proto.NullString{
					NullString: sql.NullString{
						String: "Using where; Skip_open_table; Scanned 1 database",
						Valid:  true,
					},
				},
			},
		},
		JSON: "{\n  \"query_block\": {\n    \"select_id\": 1,\n    \"table\": {\n      \"table_name\": \"tables\",\n      \"access_type\": \"ALL\",\n      \"key\": \"TABLE_NAME\",\n      \"skip_open_table\": true,\n      \"scanned_databases\": \"1\",\n      \"attached_condition\": \"(`information_schema`.`tables`.`TABLE_NAME` = 'tables')\"\n    }\n  }\n}",
	}

	explainQuery := &proto.ExplainQuery{
		ServiceInstance: s.mysqlInstance,
		Db:              db,
		Query:           query,
	}
	data, err := json.Marshal(&explainQuery)
	t.Assert(err, IsNil)

	cmd := &proto.Cmd{
		Service: "query",
		Cmd:     "Explain",
		Data:    data,
	}

	gotReply := explainService.Handle(cmd)
	t.Assert(gotReply, NotNil)
	t.Assert(gotReply.Error, Equals, "")

	var gotExplainResult *proto.ExplainResult
	err = json.Unmarshal(gotReply.Data, &gotExplainResult)
	t.Assert(err, IsNil)
	t.Assert(gotExplainResult, DeepEquals, expectedExplainResult)
}
