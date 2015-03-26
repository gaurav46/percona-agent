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

package mm

/**
 * mm is a proxy manager for monitors.  It doesn't have its own config,
 * it's job is to start and stop monitors, mostly done in Handle().
 */

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"sync"
	"time"

	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/data"
	"github.com/percona/percona-agent/instance"
	"github.com/percona/percona-agent/mrms"
	"github.com/percona/percona-agent/pct"
	"github.com/percona/percona-agent/ticker"
)

// We use one binding per unique mm.Report interval.  For example, if some monitors
// report every 60s and others every 10s, then there are two bindings.  All monitors
// with the same report interval share the same binding: collectionChan to send
// metrics and aggregator summarizing and reporting those metrics.
type Binding struct {
	aggregator     *Aggregator
	collectionChan chan *Collection // <- metrics from monitors
}

type Manager struct {
	logger  *pct.Logger
	factory MonitorFactory
	clock   ticker.Manager
	spool   data.Spooler
	im      *instance.Repo
	// --
	monitors    map[string]Monitor
	running     bool
	mux         *sync.RWMutex // guards monitors and running
	status      *pct.Status
	aggregators map[uint]*Binding
	mrm         mrms.Monitor
}

func NewManager(logger *pct.Logger, factory MonitorFactory, clock ticker.Manager, spool data.Spooler, im *instance.Repo, mrm mrms.Monitor) *Manager {
	m := &Manager{
		logger:  logger,
		factory: factory,
		clock:   clock,
		spool:   spool,
		im:      im,
		// --
		monitors:    make(map[string]Monitor),
		status:      pct.NewStatus([]string{"mm"}),
		aggregators: make(map[uint]*Binding),
		mux:         &sync.RWMutex{},
		mrm:         mrm,
	}
	return m
}

/////////////////////////////////////////////////////////////////////////////
// Interface
/////////////////////////////////////////////////////////////////////////////

// @goroutine[0]
func (m *Manager) Start() error {
	// todo: should lock here but we call Handle() which also locks
	//m.mux.Lock()
	//defer m.mux.Unlock()

	if m.running {
		return pct.ServiceIsRunningError{Service: "mm"}
	}

	// Start all metric monitors.
	glob := filepath.Join(pct.Basedir.Dir("config"), "mm-*.conf")
	configFiles, err := filepath.Glob(glob)
	if err != nil {
		return err
	}

	for _, configFile := range configFiles {
		data, err := ioutil.ReadFile(configFile)
		if err != nil {
			m.logger.Error("Read " + configFile + ": " + err.Error())
			continue
		}
		config := &Config{}
		if err := json.Unmarshal(data, config); err != nil {
			m.logger.Error("Decode " + configFile + ": " + err.Error())
			continue
		}
		cmd := &proto.Cmd{
			Ts:   time.Now().UTC(),
			Cmd:  "StartService",
			Data: data,
		}
		reply := m.Handle(cmd)
		if reply.Error != "" {
			m.logger.Error("Start " + configFile + ": " + reply.Error)
			continue
		}
		m.logger.Info("Started " + configFile)
	}

	m.running = true

	m.logger.Info("Started")
	m.status.Update("mm", "Running")
	return nil
}

// @goroutine[0]
func (m *Manager) Stop() error {
	m.mux.Lock()
	defer m.mux.Unlock()
	for name, monitor := range m.monitors {
		m.status.Update("mm", "Stopping "+name)
		if err := monitor.Stop(); err != nil {
			m.logger.Warn("Failed to stop " + name + ": " + err.Error())
			continue
		}
		m.clock.Remove(monitor.TickChan())
		delete(m.monitors, name)
	}
	m.running = false
	m.logger.Info("Stopped")
	m.status.Update("mm", "Stopped")
	return nil
}

// @goroutine[0]
func (m *Manager) Handle(cmd *proto.Cmd) *proto.Reply {
	m.status.UpdateRe("mm", "Handling", cmd)
	defer m.status.Update("mm", "Running")

	switch cmd.Cmd {
	case "StartService":
		mm, name, err := m.getMonitorConfig(cmd)
		if err != nil {
			return cmd.Reply(nil, err)
		}

		m.status.UpdateRe("mm", "Starting "+name, cmd)
		m.logger.Info("Start", name, cmd)

		// Monitors names must be unique.
		m.mux.RLock()
		_, haveMonitor := m.monitors[name]
		m.mux.RUnlock()
		if haveMonitor {
			return cmd.Reply(nil, errors.New("Duplicate monitor: "+name))
		}

		// Create the monitor based on its type.
		monitor, err := m.factory.Make(mm.Service, mm.InstanceId, cmd.Data)
		if err != nil {
			return cmd.Reply(nil, errors.New("Factory: "+err.Error()))
		}

		// Make synchronized (3rd arg=true) ticker for collect interval.  It's
		// synchronized so all data aligns in charts, else we can get MySQL metrics
		// at 00:03 and system metrics at 00:05 and other metrics at 00:06 which
		// makes it very difficult to see all metrics at a single point in time
		// or meaningfully compare a single interval, e.g. 00:00 to 00:05.
		tickChan := make(chan time.Time)
		m.clock.Add(tickChan, mm.Collect, true)

		// We need one aggregator for each unique report interval.  There's usually
		// just one: 60s.  Remember: report interval != collect interval.  Monitors
		// can collect at different intervals (typically 1s and 10s), yet all report
		// at the same 60s interval, or different report intervals.
		a, ok := m.aggregators[mm.Report]
		if !ok {
			// Make new aggregator for this report interval.
			logger := pct.NewLogger(m.logger.LogChan(), fmt.Sprintf("mm-ag-%d", mm.Report))
			collectionChan := make(chan *Collection, 5)
			aggregator := NewAggregator(logger, int64(mm.Report), collectionChan, m.spool)
			aggregator.Start()

			// Save aggregator for other monitors with same report interval.
			a = &Binding{aggregator, collectionChan}
			m.aggregators[mm.Report] = a
			m.logger.Info("Created", mm.Report, "second aggregator")
		}

		// Start the monitor.
		if err := monitor.Start(tickChan, a.collectionChan); err != nil {
			return cmd.Reply(nil, errors.New("Start "+name+": "+err.Error()))
		}
		m.mux.Lock()
		m.monitors[name] = monitor
		m.mux.Unlock()

		// Save the monitor-specific config to disk so agent starts on restart.
		monitorConfig := monitor.Config()
		if err := pct.Basedir.WriteConfig(name, monitorConfig); err != nil {
			return cmd.Reply(nil, errors.New("Write "+name+" config:"+err.Error()))
		}

		return cmd.Reply(nil) // success
	case "StopService":
		_, name, err := m.getMonitorConfig(cmd)
		if err != nil {
			return cmd.Reply(nil, err)
		}
		m.status.UpdateRe("mm", "Stopping "+name, cmd)
		m.logger.Info("Stop", name, cmd)
		m.mux.RLock()
		monitor, ok := m.monitors[name]
		m.mux.RUnlock()
		if !ok {
			return cmd.Reply(nil, errors.New("Unknown monitor: "+name))
		}
		if err := monitor.Stop(); err != nil {
			return cmd.Reply(nil, errors.New("Stop "+name+": "+err.Error()))
		}
		m.clock.Remove(monitor.TickChan())
		if err := pct.Basedir.RemoveConfig(name); err != nil {
			return cmd.Reply(nil, errors.New("Remove "+name+": "+err.Error()))
		}
		m.mux.Lock()
		delete(m.monitors, name)
		m.mux.Unlock()
		return cmd.Reply(nil) // success
	case "GetConfig":
		config, errs := m.GetConfig()
		return cmd.Reply(config, errs...)
	default:
		// SetConfig does not work by design.  To re-configure a monitor,
		// stop it then start it again with the new config.
		return cmd.Reply(nil, pct.UnknownCmdError{Cmd: cmd.Cmd})
	}
}

// @goroutine[1]
func (m *Manager) Status() map[string]string {
	status := m.status.All()
	m.mux.RLock()
	defer m.mux.RUnlock()
	for _, monitor := range m.monitors {
		monitorStatus := monitor.Status()
		for k, v := range monitorStatus {
			status[k] = v
		}
	}
	return status
}

func (m *Manager) GetConfig() ([]proto.AgentConfig, []error) {
	m.logger.Debug("GetConfig:call")
	defer m.logger.Debug("GetConfig:return")

	m.mux.RLock()
	defer m.mux.RUnlock()

	// Manager does not have its own config.  It returns all monitors' configs instead.

	// Configs are always returned as array of AgentConfig resources.
	configs := []proto.AgentConfig{}
	errs := []error{}
	for _, monitor := range m.monitors {
		monitorConfig := monitor.Config()
		// Full monitor config as JSON string.
		bytes, err := json.Marshal(monitorConfig)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		// Just the monitor's ServiceInstance, aka ExternalService.
		mmConfig := &Config{}
		if err := json.Unmarshal(bytes, mmConfig); err != nil {
			errs = append(errs, err)
			continue
		}
		config := proto.AgentConfig{
			InternalService: "mm",
			ExternalService: proto.ServiceInstance{
				Service:    mmConfig.Service,
				InstanceId: mmConfig.InstanceId,
			},
			Config:  string(bytes),
			Running: true, // config removed if stopped, so it must be running
		}
		configs = append(configs, config)
	}

	return configs, errs
}

func (m *Manager) getMonitorConfig(cmd *proto.Cmd) (*Config, string, error) {
	/**
	 * cmd.Data is a monitor-specific config, e.g. mysql.Config.  But monitor-specific
	 * configs embed mm.Config, so get that first to determine the monitor's name and
	 * type which is all we need to start it.  The monitor itself will decode cmd.Data
	 * into it's specific config, which we fetch back later by calling monitor.Config()
	 * to save to disk.
	 */
	mm := &Config{}
	if cmd.Data != nil {
		if err := json.Unmarshal(cmd.Data, mm); err != nil {
			return nil, "", errors.New("mm.getMonitorConfig:json.Unmarshal:" + err.Error())
		}
	}

	// The real name of the internal service, e.g. mm-mysql-1:
	// TODO: FIX THIS - COMMENTED ON instance refactor
	//name := "mm-" + m.im.Name(mm.InstanceId)
	name := ""
	return mm, name, nil
}
