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

package sysconfig

/**
 * sysconfig is a proxy manager for monitors.  It implements the service manager
 * interface (pct/service.go), but it's always running.  Its main job is done in
 * Handle(): keeping track of the monitors it starts and stops.
 */

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"path/filepath"
	"sync"
	"time"

	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/data"
	"github.com/percona/percona-agent/instance"
	"github.com/percona/percona-agent/pct"
	"github.com/percona/percona-agent/ticker"
)

type Manager struct {
	logger  *pct.Logger
	factory MonitorFactory
	clock   ticker.Manager
	spool   data.Spooler
	im      *instance.Repo
	// --
	monitors       map[string]Monitor
	running        bool
	mux            *sync.RWMutex // guards monitors and running
	reportChan     chan *Report  // <- Report from monitor
	spoolerRunning bool
	status         *pct.Status
}

func NewManager(logger *pct.Logger, factory MonitorFactory, clock ticker.Manager, spool data.Spooler, im *instance.Repo) *Manager {
	m := &Manager{
		logger:  logger,
		factory: factory,
		clock:   clock,
		spool:   spool,
		im:      im,
		// --
		reportChan: make(chan *Report, 3),
		monitors:   make(map[string]Monitor),
		status:     pct.NewStatus([]string{"sysconfig", "sysconfig-spooler"}),
		mux:        &sync.RWMutex{},
	}
	return m
}

/////////////////////////////////////////////////////////////////////////////
// Interface
/////////////////////////////////////////////////////////////////////////////

func (m *Manager) Start() error {
	if m.running {
		return pct.ServiceIsRunningError{Service: "sysconfig"}
	}

	if !m.spoolerRunning {
		go m.spooler()
		m.spoolerRunning = true
	}

	// Start all sysconfig monitors.
	glob := filepath.Join(pct.Basedir.Dir("config"), "sysconfig-*.conf")
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
			m.logger.Error("Start " + configFile + ": " + err.Error())
			continue
		}
		m.logger.Info("Started " + configFile)
	}

	m.running = true

	m.logger.Info("Started")
	m.status.Update("sysconfig", "Running")
	return nil
}

// @goroutine[0]
func (m *Manager) Stop() error {
	m.mux.Lock()
	defer m.mux.Unlock()
	for name, monitor := range m.monitors {
		m.status.Update("sysconfig", "Stopping "+name)
		if err := monitor.Stop(); err != nil {
			m.logger.Warn("Failed to stop " + name + ": " + err.Error())
			continue
		}
		m.clock.Remove(monitor.TickChan())
		delete(m.monitors, name)
	}
	m.running = false
	m.logger.Info("Stopped")
	m.status.Update("sysconfig", "Stopped")
	return nil
}

// @goroutine[0]
func (m *Manager) Handle(cmd *proto.Cmd) *proto.Reply {
	m.status.UpdateRe("sysconfig", "Handling", cmd)
	defer m.status.Update("sysconfig", "Running")

	switch cmd.Cmd {
	case "StartService":
		c, name, err := m.getMonitorConfig(cmd)
		if err != nil {
			return cmd.Reply(nil, err)
		}

		m.status.UpdateRe("sysconfig", "Starting "+name, cmd)
		m.logger.Info("Start", name, cmd)

		// Monitors names must be unique.
		m.mux.RLock()
		_, haveMonitor := m.monitors[name]
		m.mux.RUnlock()
		if haveMonitor {
			return cmd.Reply(nil, errors.New("Duplicate monitor: "+name))
		}

		// Create the monitor based on its type.
		var monitor Monitor
		if monitor, err = m.factory.Make(c.Service, c.InstanceId, cmd.Data); err != nil {
			return cmd.Reply(nil, errors.New("Factory: "+err.Error()))
		}

		// Make unsynchronized (3rd arg=false) ticker for collect interval,
		// it's unsynchronized because 1) we don't need sysconfig data to be
		// synchronized, and 2) sysconfig monitors usually collect very slowly,
		// e.g. 1h, so if we synced it it could wait awhile before 1st tick.
		tickChan := make(chan time.Time)
		m.clock.Add(tickChan, c.Report, false)

		// Start the monitor.
		if err = monitor.Start(tickChan, m.reportChan); err != nil {
			return cmd.Reply(nil, errors.New("Start "+name+": "+err.Error()))
		}
		m.mux.Lock()
		m.monitors[name] = monitor
		m.mux.Unlock()

		// Save the monitor-specific config to disk so agent starts on restart.
		monitorConfig := monitor.Config()
		if err = pct.Basedir.WriteConfig(name, monitorConfig); err != nil {
			return cmd.Reply(nil, errors.New("Write "+name+" config:"+err.Error()))
		}
		return cmd.Reply(nil) // success
	case "StopService":
		_, name, err := m.getMonitorConfig(cmd)
		if err != nil {
			return cmd.Reply(nil, err)
		}
		m.status.UpdateRe("sysconfig", "Stopping "+name, cmd)
		m.logger.Info("Stop", name, cmd)
		m.mux.RLock()
		monitor, ok := m.monitors[name]
		m.mux.RUnlock()
		if !ok {
			return cmd.Reply(nil, errors.New("Unknown monitor: "+name))
		}
		if err = monitor.Stop(); err != nil {
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
			InternalService: "sysconfig",
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

// --------------------------------------------------------------------------

func (m *Manager) spooler() {
	defer func() {
		if err := recover(); err != nil {
			m.logger.Error("Sysconfig spooler crashed: ", err)
		}
		m.status.Update("sysconfig-spooler", "Stopped")
	}()
	m.status.Update("sysconfig-spooler", "Running")
	for s := range m.reportChan {
		if err := m.spool.Write("sysconfig", s); err != nil {
			m.logger.Warn("Lost report:", err)
		}
	}
}

func (m *Manager) getMonitorConfig(cmd *proto.Cmd) (*Config, string, error) {
	/**
	 * cmd.Data is a monitor-specific config, e.g. mysql.Config.  But monitor-specific
	 * configs embed c.Config, so get that first to determine the monitor's name and
	 * type which is all we need to start it.  The monitor itself will decode cmd.Data
	 * into it's specific config, which we fetch back later by calling monitor.Config()
	 * to save to disk.
	 */
	c := &Config{}
	if err := json.Unmarshal(cmd.Data, c); err != nil {
		return nil, "", errors.New("sysconfig.Handle:json.Unmarshal:" + err.Error())
	}

	// The real name of the internal service, e.g. sysconfig-mysql-1:
	// TODO: FIX THIS, COMMENTED ON REFACTOR OF INSTANCE REPO
	//name := "sysconfig-" + m.im.Name(c.Service, c.InstanceId)
	name := ""
	return c, name, nil
}
