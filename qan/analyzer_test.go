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

package qan_test

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"time"

	. "github.com/go-test/test"
	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/instance"
	"github.com/percona/percona-agent/mysql"
	"github.com/percona/percona-agent/pct"
	"github.com/percona/percona-agent/qan"
	"github.com/percona/percona-agent/qan/slowlog"
	"github.com/percona/percona-agent/test"
	"github.com/percona/percona-agent/test/mock"
	. "gopkg.in/check.v1"
)

type AnalyzerTestSuite struct {
	nullmysql     *mock.NullMySQL
	iter          *mock.Iter
	spool         *mock.Spooler
	clock         *mock.Clock
	api           *mock.API
	worker        *mock.QanWorker
	restartChan   chan bool
	logChan       chan *proto.LogEntry
	logger        *pct.Logger
	intervalChan  chan *qan.Interval
	dataChan      chan interface{}
	tmpDir        string
	configDir     string
	im            *instance.Repo
	mysqlInstance proto.ServiceInstance
	config        qan.Config
}

var _ = Suite(&AnalyzerTestSuite{})

// The highest possible value max_slowlog_size can be set to (from Percona Server documentation)
const MAX_SLOW_LOG_SIZE int64 = 1073741824

func (s *AnalyzerTestSuite) SetUpSuite(t *C) {
	s.nullmysql = mock.NewNullMySQL()

	s.logChan = make(chan *proto.LogEntry, 1000)
	s.logger = pct.NewLogger(s.logChan, "qan-test")

	s.intervalChan = make(chan *qan.Interval, 1)

	s.iter = mock.NewIter(s.intervalChan)

	s.dataChan = make(chan interface{}, 1)
	s.spool = mock.NewSpooler(s.dataChan)

	var err error
	s.tmpDir, err = ioutil.TempDir("/tmp", "agent-test")
	t.Assert(err, IsNil)

	if err := pct.Basedir.Init(s.tmpDir); err != nil {
		t.Fatal(err)
	}
	s.configDir = pct.Basedir.Dir("config")

	s.im = instance.NewRepo(pct.NewLogger(s.logChan, "im-test"), s.configDir, s.api)
	data, err := json.Marshal(&proto.MySQLInstance{
		Hostname: "bm-cloud-db01",
		Alias:    "db01",
		DSN:      "user:pass@tcp/",
	})
	t.Assert(err, IsNil)
	s.im.Add("mysql", 1, data, false)
	s.mysqlInstance = proto.ServiceInstance{Service: "mysql", InstanceId: 1}

	links := map[string]string{
		"agent":     "http://localhost/agent",
		"instances": "http://localhost/instances",
	}
	s.api = mock.NewAPI("http://localhost", "http://localhost", "123", "abc-123-def", links)

	s.restartChan = make(chan bool, 1)
}

func (s *AnalyzerTestSuite) SetUpTest(t *C) {
	s.nullmysql.Reset()
	s.iter.Reset()
	s.spool.Reset()
	s.clock = mock.NewClock()
	if err := test.ClearDir(s.configDir, "*"); err != nil {
		t.Fatal(err)
	}
	s.worker = mock.NewQanWorker()
	// Config needs to be recreated on every test since it can be modified by the test analyzers
	s.config = qan.Config{
		ServiceInstance: s.mysqlInstance,
		CollectFrom:     "slowlog",
		Interval:        60,
		WorkerRunTime:   60,
		MaxSlowLogSize:  MAX_SLOW_LOG_SIZE,
		Start: []mysql.Query{
			mysql.Query{Set: "-- start"},
		},
		Stop: []mysql.Query{
			mysql.Query{Set: "-- stop"},
		},
	}
}

func (s *AnalyzerTestSuite) TearDownSuite(t *C) {
	if err := os.RemoveAll(s.tmpDir); err != nil {
		t.Error(err)
	}
}

// --------------------------------------------------------------------------

func (s *AnalyzerTestSuite) TestRunMockWorker(t *C) {
	a := qan.NewRealAnalyzer(
		pct.NewLogger(s.logChan, "qan-analyzer"),
		s.config,
		s.iter,
		s.nullmysql,
		s.restartChan,
		s.worker,
		s.clock,
		s.spool,
	)

	err := a.Start()
	t.Assert(err, IsNil)
	test.WaitStatus(1, a, "qan-analyzer", "Idle")

	// No interval yet, so work should not have one.
	t.Check(s.worker.Interval, IsNil)

	// Send an interval. The analyzer runs the worker with it.
	now := time.Now()
	i := &qan.Interval{
		Number:      1,
		StartTime:   now,
		StopTime:    now.Add(1 * time.Minute),
		Filename:    "slow.log",
		StartOffset: 0,
		EndOffset:   999,
	}
	s.intervalChan <- i

	if !test.WaitState(s.worker.SetupChan) {
		t.Fatal("Timeout waiting for <-s.worker.SetupChan")
	}

	t.Check(s.worker.Interval, DeepEquals, i)

	if !test.WaitState(s.worker.RunChan) {
		t.Fatal("Timeout waiting for <-s.worker.SetupChan")
	}

	if !test.WaitState(s.worker.CleanupChan) {
		t.Fatal("Timeout waiting for <-s.worker.SetupChan")
	}

	err = a.Stop()
	t.Assert(err, IsNil)
	test.WaitStatus(1, a, "qan-analyzer", "Stopped")

	t.Check(a.String(), Equals, "qan-analyzer")
}

func (s *AnalyzerTestSuite) TestStartServiceFast(t *C) {
	// Simulate the next tick being 3m away (mock.clock.Eta = 180) so that
	// run() sends the first tick on the tick chan, causing the first
	// interval to start immediately.
	s.clock.Eta = 180
	defer func() { s.clock.Eta = 0 }()

	config := s.config
	config.Interval = 300 // 5m
	a := qan.NewRealAnalyzer(
		pct.NewLogger(s.logChan, "qan-analyzer"),
		config,
		s.iter,
		s.nullmysql,
		s.restartChan,
		s.worker,
		s.clock,
		s.spool,
	)
	err := a.Start()
	t.Assert(err, IsNil)
	test.WaitStatus(1, a, "qan-analyzer", "Idle")

	// run() should prime the tickChan with the 1st tick immediately.  This makes
	// the interval iter start the interval immediately.  Then run() continues
	// waiting for the iter to send an interval which happens when the real ticker
	// (the clock) sends the 2nd tick which is synced to the interval, thus ending
	// the first interval started by run() and starting the 2nd interval as normal.
	select {
	case tick := <-s.iter.TickChan():
		t.Check(tick.IsZero(), Not(Equals), true)
	case <-time.After(1 * time.Second):
		t.Error("Timeout waiting for primer tick")
	}

	// Status indicates that next interval is 3m away as we faked.
	status := a.Status()
	t.Check(status["qan-analyzer-next-interval"], Equals, "180.0s")

	err = a.Stop()
	t.Assert(err, IsNil)
}

func (s *AnalyzerTestSuite) TestMySQLRestart(t *C) {
	a := qan.NewRealAnalyzer(
		pct.NewLogger(s.logChan, "qan-analyzer"),
		s.config,
		s.iter,
		s.nullmysql,
		s.restartChan,
		s.worker,
		s.clock,
		s.spool,
	)
	err := a.Start()
	t.Assert(err, IsNil)
	test.WaitStatus(1, a, "qan-analyzer", "Idle")

	// When analyzer starts, it configures MySQL using the Start set of queries.
	got := s.nullmysql.GetSet()
	expect := s.config.Start
	if same, diff := IsDeeply(got, expect); !same {
		Dump(got)
		t.Error(diff)
	}

	// Analyzer starts its iter when MySQL is ready.
	t.Check(s.iter.Calls(), DeepEquals, []string{"Start"})
	s.iter.Reset()

	// Simulate a MySQL restart. This causes the analyzer to re-configure MySQL
	// using the same Start queries.
	s.nullmysql.Reset()
	s.restartChan <- true
	if !test.WaitState(s.nullmysql.SetChan) {
		t.Error("Timeout waiting for <-s.nullmysql.SetChan")
	}
	test.WaitStatus(1, a, "qan-analyzer", "Idle")
	got = s.nullmysql.GetSet()
	if same, diff := IsDeeply(got, expect); !same {
		Dump(got)
		t.Error(diff)
	}

	// Analyzer stops and re-starts its iter on MySQL restart.
	t.Check(s.iter.Calls(), DeepEquals, []string{"Stop", "Start"})

	s.nullmysql.Reset()
	// Enable slowlog DB rotation by setting max_slowlog_size to a value > 4096 and simulate MySQL restart
	s.nullmysql.SetGlobalVarNumber("max_slowlog_size", 100000)
	s.restartChan <- true
	if !test.WaitState(s.nullmysql.SetChan) {
		t.Error("Timeout waiting for <-s.nullmysql.SetChan")
	}
	test.WaitStatus(1, a, "qan-analyzer", "Idle")
	expectedQueries := []mysql.Query{
		mysql.Query{
			Set:    "SET GLOBAL max_slowlog_size = 0",
			Verify: "",
			Expect: "",
		},
		mysql.Query{
			Set:    "-- start",
			Verify: "",
			Expect: "",
		},
	}

	t.Check(s.nullmysql.GetSet(), DeepEquals, expectedQueries)
	t.Check(a.Config().MaxSlowLogSize, Equals, int64(100000))
	err = a.Stop()
	t.Assert(err, IsNil)
}

func (s *AnalyzerTestSuite) TestRealSlowLogWorker(t *C) {
	dsn := os.Getenv("PCT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("PCT_TEST_MYSQL_DSN is not set")
	}
	realmysql := mysql.NewConnection(dsn)
	if err := realmysql.Connect(1); err != nil {
		t.Fatal(err)
	}
	realmysql.Close()

	defer test.DrainRecvData(s.dataChan)

	config := s.config
	config.Start = []mysql.Query{
		mysql.Query{Set: "SET GLOBAL slow_query_log=OFF"},
		mysql.Query{Set: "SET GLOBAL long_query_time=0"},
		mysql.Query{Set: "SET GLOBAL slow_query_log=ON"},
	}
	config.Stop = []mysql.Query{
		mysql.Query{Set: "SET GLOBAL slow_query_log=OFF"},
		mysql.Query{Set: "SET GLOBAL long_query_time=10"},
	}

	worker := slowlog.NewWorker(pct.NewLogger(s.logChan, "qan-worker"), config, realmysql)
	//intervalChan := make(chan *qan.Interval, 1)
	//iter := mock.NewIter(intervalChan)

	a := qan.NewRealAnalyzer(
		pct.NewLogger(s.logChan, "qan-analyzer"),
		config,
		s.iter,
		realmysql,
		s.restartChan,
		worker,
		s.clock,
		s.spool,
	)
	err := a.Start()
	t.Assert(err, IsNil)
	if !test.WaitStatus(3, a, "qan-analyzer", "Idle") {
		t.Fatal("Timeout waiting for qan-analyzer=Idle")
	}

	now := time.Now().UTC()
	i := &qan.Interval{
		Number:      1,
		StartTime:   now,
		StopTime:    now.Add(1 * time.Minute),
		Filename:    inputDir + "slow001.log",
		StartOffset: 0,
		EndOffset:   524,
	}
	s.intervalChan <- i
	data := test.WaitData(s.dataChan)
	t.Assert(data, HasLen, 1)
	res := data[0].(*qan.Report)
	t.Check(res.Global.TotalQueries, Equals, uint64(2))

	err = a.Stop()
	t.Assert(err, IsNil)
}

func (s *AnalyzerTestSuite) TestRecoverWorkerPanic(t *C) {
	a := qan.NewRealAnalyzer(
		pct.NewLogger(s.logChan, "qan-analyzer"),
		s.config,
		s.iter,
		s.nullmysql,
		s.restartChan,
		s.worker,
		s.clock,
		s.spool,
	)

	err := a.Start()
	t.Assert(err, IsNil)
	test.WaitStatus(1, a, "qan-analyzer", "Idle")

	// This will cause the worker to panic when it's ran by the analyzer.
	s.worker.SetupCrashChan <- true

	// Send an interval. The analyzer runs the worker with it.
	now := time.Now()
	i := &qan.Interval{
		Number:      1,
		StartTime:   now,
		StopTime:    now.Add(1 * time.Minute),
		Filename:    "slow.log",
		StartOffset: 0,
		EndOffset:   999,
	}
	s.intervalChan <- i

	if !test.WaitState(s.worker.SetupChan) {
		t.Fatal("Timeout waiting for <-s.worker.SetupChan")
	}

	// Wait for that ^ run of the worker to fully stop and return.
	test.WaitStatus(1, a, "qan-analyzer", "Idle")

	i = &qan.Interval{
		Number:      2,
		StartTime:   now,
		StopTime:    now.Add(1 * time.Minute),
		Filename:    "slow.log",
		StartOffset: 1000,
		EndOffset:   2000,
	}
	s.intervalChan <- i

	if !test.WaitState(s.worker.SetupChan) {
		t.Fatal("Timeout waiting for <-s.worker.SetupChan")
	}

	t.Check(s.worker.Interval, DeepEquals, i)

	if !test.WaitState(s.worker.RunChan) {
		t.Fatal("Timeout waiting for <-s.worker.SetupChan")
	}

	if !test.WaitState(s.worker.CleanupChan) {
		t.Fatal("Timeout waiting for <-s.worker.SetupChan")
	}

	err = a.Stop()
	t.Assert(err, IsNil)
	test.WaitStatus(1, a, "qan-analyzer", "Stopped")

	t.Check(a.String(), Equals, "qan-analyzer")
}

// Test that a disabled slow log rotation in Percona Server (or MySQL) does not change analizer config
func (s *AnalyzerTestSuite) TestNoSlowLogTakeOver(t *C) {

	/*
		PS can be configured to rotate slow log, making qan break.
		Since qan cannot handle the situation where a slow log is rotated by a third party we take over Percona Server
		rotation and disable it on DB.
	*/
	a := qan.NewRealAnalyzer(
		pct.NewLogger(s.logChan, "qan-analyzer"),
		s.config,
		s.iter,
		s.nullmysql,
		s.restartChan,
		s.worker,
		s.clock,
		s.spool,
	)

	err := a.Start()
	t.Assert(err, IsNil)
	test.WaitStatus(1, a, "qan-analyzer", "Idle")

	// Disable DB rotation by setting max_slowlog_size to a value < 4096
	s.nullmysql.SetGlobalVarNumber("max_slowlog_size", 1000)
	// Trigger our PS slow log rotation take-over, everything should stay the same since max_slowlog_size is < 4096
	a.TakeOverPerconaServerRotation()
	t.Check(a.Config().MaxSlowLogSize, Equals, MAX_SLOW_LOG_SIZE)
	err = a.Stop()
	t.Assert(err, IsNil)
	test.WaitStatus(1, a, "qan-analyzer", "Stopped")
	t.Check(a.String(), Equals, "qan-analyzer")
}

// Test slow log rotation take over from Percona Server
func (s *AnalyzerTestSuite) TestSlowLogTakeOver(t *C) {
	a := qan.NewRealAnalyzer(
		pct.NewLogger(s.logChan, "qan-analyzer"),
		s.config,
		s.iter,
		s.nullmysql,
		s.restartChan,
		s.worker,
		s.clock,
		s.spool,
	)

	err := a.Start()
	t.Assert(err, IsNil)
	test.WaitStatus(1, a, "qan-analyzer", "Idle")

	s.nullmysql.Reset()
	// Increase our max_slowlog_size in mocked DB
	s.nullmysql.SetGlobalVarNumber("max_slowlog_size", 5000)
	// Trigger slowlog rotation, takeover should succeed since max_slowlog_size >= 4096
	a.TakeOverPerconaServerRotation()
	expectedQueries := []mysql.Query{
		mysql.Query{
			Set:    "SET GLOBAL max_slowlog_size = 0",
			Verify: "",
			Expect: "",
		},
	}

	t.Check(s.nullmysql.GetSet(), DeepEquals, expectedQueries)
	// Config should now have the configured Percona Server slow log rotation file size
	t.Check(a.Config().MaxSlowLogSize, Equals, int64(5000))

	err = a.Stop()
	t.Assert(err, IsNil)
	test.WaitStatus(1, a, "qan-analyzer", "Stopped")
	t.Check(a.String(), Equals, "qan-analyzer")
}
