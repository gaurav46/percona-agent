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

package qan

import (
	"fmt"
	"sync"
	"time"

	"github.com/percona/percona-agent/data"
	"github.com/percona/percona-agent/mysql"
	"github.com/percona/percona-agent/pct"
	"github.com/percona/percona-agent/ticker"
)

// A Worker gets queries, aggregates them, and returns a Result. Workers are ran
// by Analyzers. When ran, MySQL is presumed to be configured and ready.
type Worker interface {
	Setup(*Interval) error
	Run() (*Result, error)
	Stop() error
	Cleanup() error
	Status() map[string]string
}

// An Analyzer runs a Worker at each Interval. Analyzers are responsible for
// MySQL: configuring, restarts, etc. The Worker is only ran when MySQL is
// configured and ready. Analyzers are also responsible for making Reports from
// the Results returned by Workers. The Worker determines the type of Analyzer:
// slowlog or perfschema. Analyzers are ran by the QAN Manager.
type Analyzer interface {
	Start() error
	Stop() error
	Status() map[string]string
	String() string
}

// An AnalyzerFactory makes an Analyzer, real or mock.
type AnalyzerFactory interface {
	Make(config Config, name string, mysqlConn mysql.Connector, restartChan <-chan bool, tickChan chan time.Time) Analyzer
}

// --------------------------------------------------------------------------

type RealAnalyzer struct {
	logger      *pct.Logger
	config      Config
	iter        IntervalIter
	mysqlConn   mysql.Connector
	restartChan <-chan bool
	worker      Worker
	clock       ticker.Manager
	spool       data.Spooler
	// --
	name                string
	mysqlConfiguredChan chan bool
	workerDoneChan      chan *Interval
	status              *pct.Status
	runSync             *pct.SyncChan
	configureMySQLSync  *pct.SyncChan
	running             bool
	mux                 *sync.RWMutex
}

func NewRealAnalyzer(logger *pct.Logger, config Config, iter IntervalIter, mysqlConn mysql.Connector, restartChan <-chan bool, worker Worker, clock ticker.Manager, spool data.Spooler) *RealAnalyzer {
	name := logger.Service()
	a := &RealAnalyzer{
		logger:      logger,
		config:      config,
		iter:        iter,
		mysqlConn:   mysqlConn,
		restartChan: restartChan,
		worker:      worker,
		clock:       clock,
		spool:       spool,
		// --
		name:                name,
		mysqlConfiguredChan: make(chan bool, 1),
		workerDoneChan:      make(chan *Interval, 1),
		status:              pct.NewStatus([]string{name, name + "-last-interval", name + "-next-interval"}),
		runSync:             pct.NewSyncChan(),
		configureMySQLSync:  pct.NewSyncChan(),
		mux:                 &sync.RWMutex{},
	}
	return a
}

func (a *RealAnalyzer) String() string {
	return a.logger.Service()
}

func (a *RealAnalyzer) Start() error {
	a.logger.Debug("Start:call")
	defer a.logger.Debug("Start:return")
	a.mux.Lock()
	defer a.mux.Unlock()
	if a.running {
		return nil
	}
	go a.run()
	a.running = true
	return nil
}

func (a *RealAnalyzer) Stop() error {
	a.logger.Debug("Stop:call")
	defer a.logger.Debug("Stop:return")
	a.mux.Lock()
	defer a.mux.Unlock()
	if !a.running {
		return nil
	}
	a.runSync.Stop()
	a.runSync.Wait()
	a.running = false
	return nil
}

func (a *RealAnalyzer) Status() map[string]string {
	a.mux.RLock()
	defer a.mux.RUnlock()
	if a.running {
		a.status.Update(a.name+"-next-interval", fmt.Sprintf("%.1fs", a.clock.ETA(a.iter.TickChan())))
	} else {
		a.status.Update(a.name+"-next-interval", "")
	}
	return a.status.Merge(a.worker.Status())
}

// --------------------------------------------------------------------------

func (a *RealAnalyzer) configureMySQL(config []mysql.Query, tryLimit int) {
	a.logger.Debug("configureMySQL:call")
	defer func() {
		if err := recover(); err != nil {
			a.logger.Error(a.name+":configureMySQL crashed: ", err)
		}
		a.logger.Debug("configureMySQL:return")
	}()

	try := 0
	for (tryLimit == 0) || (try <= tryLimit) {
		try++

		select {
		case <-a.configureMySQLSync.StopChan:
			a.logger.Debug("configureMySQL:stop")
			a.configureMySQLSync.Done()
			return
		default:
		}

		// Connect handles backoff internally.
		a.logger.Debug("configureMySQL:connecting")
		if err := a.mysqlConn.Connect(1); err != nil {
			a.logger.Warn("Cannot connect to MySQL:", err)
			continue
		}
		defer a.mysqlConn.Close()

		a.logger.Debug("configureMySQL:configuring")
		if err := a.mysqlConn.Set(config); err != nil {
			a.mysqlConn.Close()
			a.logger.Warn("Cannot configure MySQL:", err)
			continue
		}

		a.logger.Debug("configureMySQL:configured")
		select {
		case a.mysqlConfiguredChan <- true:
			return // success
		case <-a.configureMySQLSync.StopChan:
			a.logger.Debug("configureMySQL:stop")
			a.configureMySQLSync.Done()
			return
		}
	}
}

func (a *RealAnalyzer) run() {
	a.logger.Debug("run:call")
	defer a.logger.Debug("run:return")

	mysqlConfigured := false
	go a.configureMySQL(a.config.Start, 0) // try forever

	defer func() {
		a.status.Update(a.name, "Stopping worker")
		a.logger.Info("Stopping worker")
		a.worker.Stop()

		a.status.Update(a.name, "Stopping interval iter")
		a.logger.Info("Stopping interval iter")
		a.iter.Stop()

		if !mysqlConfigured {
			a.status.Update(a.name, "Stopping MySQL config")
			a.logger.Info("Stopping MySQL config")
			a.configureMySQLSync.Stop()
			a.configureMySQLSync.Wait()
		}

		a.status.Update(a.name, "Stopping QAN on MySQL")
		a.logger.Info("Stopping QAN on MySQL")
		a.configureMySQL(a.config.Stop, 1) // try once

		if err := recover(); err != nil {
			a.logger.Error(a.name+" crashed: ", err)
			a.status.Update(a.name, "Crashed")
		} else {
			a.status.Update(a.name, "Stopped")
		}

		a.runSync.Done()
	}()

	workerRunning := false
	lastTs := time.Time{}
	currentInterval := &Interval{}
	for {
		a.logger.Debug("run:idle")
		if mysqlConfigured {
			a.status.Update(a.name, "Idle")
		} else {
			a.status.Update(a.name, "Idle (MySQL not configured)")
		}

		select {
		case interval := <-a.iter.IntervalChan():
			if !mysqlConfigured {
				a.logger.Debug(fmt.Sprintf("run:interval:%d:skip (mysql not configured)", interval.Number))
				continue
			}

			if workerRunning {
				a.logger.Warn(fmt.Sprintf("Skipping interval '%s' because interval '%s' is still being parsed"),
					currentInterval, interval)
				continue
			}

			a.status.Update(a.name, fmt.Sprintf("Starting interval '%s'", interval))
			a.logger.Debug(fmt.Sprintf("run:interval:%s", interval))
			currentInterval = interval

			// Let worker do whatever it needs before it starts processing
			// the interval. This mostly makes testing easier.
			if err := a.worker.Setup(interval); err != nil {
				a.logger.Warn(err)
				continue
			}

			// Run the worker, timing it, make a report from its results, spool
			// the report. When done the interval is returned on workerDoneChan.
			go a.runWorker(interval)
			workerRunning = true
		case interval := <-a.workerDoneChan:
			a.logger.Debug("run:worker:done")
			a.status.Update(a.name, fmt.Sprintf("Cleaning up after interval '%s'", interval))
			workerRunning = false

			if interval.StartTime.After(lastTs) {
				t0 := interval.StartTime.Format("2006-01-02 15:04:05")
				t1 := interval.StopTime.Format("15:04:05 MST")
				a.status.Update(a.name+"-last-interval", fmt.Sprintf("%s to %s", t0, t1))
				lastTs = interval.StartTime
			}

			// Let worker do whatever it needs after processing the interval.
			// This mostly maske testing easier.
			if err := a.worker.Cleanup(); err != nil {
				a.logger.Warn(err)
				continue
			}
		case mysqlConfigured = <-a.mysqlConfiguredChan:
			a.logger.Debug("run:mysql:configured")
			// Start the IntervalIter once MySQL has been configured.
			// This avoids no data or partial data, e.g. slow log verbosity
			// not set yet.
			a.iter.Start()
		case <-a.restartChan:
			a.logger.Debug("run:mysql:restart")
			// If MySQL is not configured, then configureMySQL() should already
			// be running, trying to configure it. Else, we need to run
			// configureMySQL again.
			if mysqlConfigured {
				mysqlConfigured = false
				a.iter.Stop()
				go a.configureMySQL(a.config.Start, 0) // try forever
			}
		case <-a.runSync.StopChan:
			a.logger.Debug("run:stop")
			return
		}
	}
}

func (a *RealAnalyzer) runWorker(interval *Interval) {
	a.logger.Debug(fmt.Sprintf("runWorker:call:%d", interval.Number))
	defer func() {
		a.logger.Debug(fmt.Sprintf("runWorker:return:%d", interval.Number))
		if err := recover(); err != nil {
			a.logger.Error(fmt.Sprintf(a.name+"-worker crashed: '%s': %s", interval, err))
		}
		a.workerDoneChan <- interval
	}()

	// Run the worker to process the interval.
	t0 := time.Now()
	result, err := a.worker.Run()
	t1 := time.Now()
	if err != nil {
		a.logger.Error(err)
		return
	}
	if result == nil {
		// This shouldn't happen. If it does, the worker has a bug because
		// it should have returned an error above.
		a.logger.Error("Nil result", interval)
		return
	}
	result.RunTime = t1.Sub(t0).Seconds()

	// Translate the results into a report and spool.
	// NOTE: "qan" here is correct; do not use a.name.
	report := MakeReport(a.config, interval, result)
	if err := a.spool.Write("qan", report); err != nil {
		a.logger.Warn("Lost report:", err)
	}
}