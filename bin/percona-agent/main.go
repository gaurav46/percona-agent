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

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	golog "log"
	"os"
	"os/signal"
	"os/user"
	"runtime"
	"syscall"
	"time"

	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/agent"
	"github.com/percona/percona-agent/client"
	"github.com/percona/percona-agent/data"
	"github.com/percona/percona-agent/instance"
	"github.com/percona/percona-agent/log"
	"github.com/percona/percona-agent/mm"
	mmMonitor "github.com/percona/percona-agent/mm/monitor"
	"github.com/percona/percona-agent/mrms"
	mrmsMonitor "github.com/percona/percona-agent/mrms/monitor"
	"github.com/percona/percona-agent/mysql"
	"github.com/percona/percona-agent/pct"
	pctCmd "github.com/percona/percona-agent/pct/cmd"
	"github.com/percona/percona-agent/qan"
	qanFactory "github.com/percona/percona-agent/qan/factory"
	"github.com/percona/percona-agent/qan/perfschema"
	"github.com/percona/percona-agent/qan/slowlog"
	"github.com/percona/percona-agent/query"
	queryService "github.com/percona/percona-agent/query/service"
	"github.com/percona/percona-agent/sysconfig"
	sysconfigMonitor "github.com/percona/percona-agent/sysconfig/monitor"
	"github.com/percona/percona-agent/sysinfo"
	mysqlSysinfo "github.com/percona/percona-agent/sysinfo/mysql"
	systemSysinfo "github.com/percona/percona-agent/sysinfo/system"
	"github.com/percona/percona-agent/ticker"
)

var (
	flagPing    bool
	flagStatus  bool
	flagBasedir string
	flagPidFile string
	flagVersion bool
)

func init() {
	golog.SetFlags(golog.Ldate | golog.Ltime | golog.Lmicroseconds | golog.Lshortfile)
	golog.SetOutput(os.Stdout)

	flag.BoolVar(&flagPing, "ping", false, "Ping API")
	flag.BoolVar(&flagStatus, "status", false, "Agent status")
	flag.StringVar(&flagBasedir, "basedir", pct.DEFAULT_BASEDIR, "Agent basedir")
	flag.StringVar(&flagPidFile, "pidfile", agent.DEFAULT_PIDFILE, "PID file")
	flag.BoolVar(&flagVersion, "version", false, "Print version")
	flag.Parse()
	// We don't accept any possitional arguments
	if len(flag.Args()) != 0 {
		flag.Usage()
		os.Exit(1)
	}
	// Below condition should allow people to use higher number of threads if they want to
	if os.Getenv("GOMAXPROCS") == "" {
		// "1" is currently default in golang but this may change in the future, so let's lock this
		runtime.GOMAXPROCS(1)
	}
}

func run() error {
	version := fmt.Sprintf("percona-agent %s%s rev %s", agent.VERSION, agent.REL, agent.REVISION)
	if flagVersion {
		fmt.Println(version)
		return nil
	}
	golog.Printf("Running %s pid %d\n", version, os.Getpid())

	if err := pct.Basedir.Init(flagBasedir); err != nil {
		return err
	}

	// Start-lock file is used to let agent1 self-update, create start-lock,
	// start updated agent2, exit cleanly, then agent2 starts.  agent1 may
	// not use a PID file, so this special file is required.
	if err := pct.WaitStartLock(); err != nil {
		return err
	}
	// NOTE: This must run last, and defer if LIFO, so it must be declared first.
	defer os.Remove(pct.Basedir.File("start-lock"))

	/**
	 * Agent config (require API key and agent UUID)
	 */

	if !pct.FileExists(pct.Basedir.ConfigFile("agent")) {
		return fmt.Errorf("Agent config file %s does not exist", pct.Basedir.ConfigFile("agent"))
	}

	bytes, err := agent.LoadConfig()
	if err != nil {
		return fmt.Errorf("Invalid agent config: %s\n", err)
	}
	agentConfig := &agent.Config{}
	if err := json.Unmarshal(bytes, agentConfig); err != nil {
		return fmt.Errorf("Error parsing "+pct.Basedir.ConfigFile("agent")+": ", err)
	}

	golog.Println("ApiHostname: " + agentConfig.ApiHostname)
	golog.Println("AgentUuid: " + agentConfig.AgentUuid)

	/**
	 * Ping and exit, maybe.
	 */

	// Set for all connections to API.  X-Percona-API-Key is set automatically
	// using the pct.APIConnector.
	headers := map[string]string{
		"X-Percona-Agent-Version": agent.VERSION,
	}

	if flagPing {
		t0 := time.Now()
		code, err := pct.Ping(agentConfig.ApiHostname, agentConfig.ApiKey, headers)
		d := time.Now().Sub(t0)
		if err != nil || code != 200 {
			return fmt.Errorf("Ping FAIL (%d %d %s)", d, code, err)
		} else {
			golog.Printf("Ping OK (%s)", d)
			return nil
		}
	}

	/**
	 * PID file
	 */

	pidFilePath := agentConfig.PidFile
	if flagPidFile != "" {
		pidFilePath = flagPidFile
	}
	if pidFilePath != "" {
		pidFile := pct.NewPidFile()
		if err := pidFile.Set(pidFilePath); err != nil {
			golog.Fatalln(err)
		}
		defer pidFile.Remove()
	}

	/**
	 * REST API
	 */

	retry := -1 // unlimited
	if flagStatus {
		retry = 1
	}
	api, err := ConnectAPI(agentConfig, retry)
	if err != nil {
		golog.Fatal(err)
	}

	// Get agent status via API and exit.
	if flagStatus {
		code, bytes, err := api.Get(agentConfig.ApiKey, api.AgentLink("self")+"/status")
		if err != nil {
			return err
		}
		if code == 404 {
			return fmt.Errorf("Agent not found")
		}
		status := make(map[string]string)
		if err := json.Unmarshal(bytes, &status); err != nil {
			return err
		}
		golog.Println(status)
		return nil
	}

	/**
	 * Connection factory
	 */
	connFactory := &mysql.RealConnectionFactory{}

	/**
	 * Log relay
	 */

	logChan := make(chan *proto.LogEntry, log.BUFFER_SIZE*3)

	// Log websocket client, possibly disabled later.
	logClient, err := client.NewWebsocketClient(pct.NewLogger(logChan, "log-ws"), api, "log", headers)
	if err != nil {
		golog.Fatalln(err)
	}
	logManager := log.NewManager(
		logClient,
		logChan,
	)
	if err := logManager.Start(); err != nil {
		return fmt.Errorf("Error starting logmanager: %s\n", err)
	}

	/**
	 * MRMS (MySQL Restart Monitoring Service)
	 */
	mrm := mrmsMonitor.NewMonitor(
		pct.NewLogger(logChan, "mrms-monitor"),
		connFactory,
	)
	mrmsManager := mrms.NewManager(
		pct.NewLogger(logChan, "mrms-manager"),
		mrm,
	)
	if err := mrmsManager.Start(); err != nil {
		return fmt.Errorf("Error starting mrms manager: %s\n", err)
	}

	/**
	 * Instance manager
	 */
	itManager := instance.NewManager(
		pct.NewLogger(logChan, "instance-manager"),
		pct.Basedir.Dir("config"),
		api,
		mrm,
	)
	if err := itManager.Start(); err != nil {
		return fmt.Errorf("Error starting instance manager: %s\n", err)
	}

	/**
	 * Data spooler and sender
	 */

	hostname, _ := os.Hostname()

	dataClient, err := client.NewWebsocketClient(pct.NewLogger(logChan, "data-ws"), api, "data", headers)
	if err != nil {
		golog.Fatalln(err)
	}
	dataManager := data.NewManager(
		pct.NewLogger(logChan, "data"),
		pct.Basedir.Dir("data"),
		pct.Basedir.Dir("trash"),
		hostname,
		dataClient,
	)
	if err := dataManager.Start(); err != nil {
		return fmt.Errorf("Error starting data manager: %s\n", err)
	}

	/**
	 * Collecct/report ticker (master clock)
	 */

	nowFunc := func() int64 { return time.Now().UTC().UnixNano() }
	clock := ticker.NewClock(&ticker.RealTickerFactory{}, nowFunc)

	/**
	 * Metric and system config monitors
	 */

	mmManager := mm.NewManager(
		pct.NewLogger(logChan, "mm"),
		mmMonitor.NewFactory(logChan, itManager.Repo(), mrm),
		clock,
		dataManager.Spooler(),
		itManager.Repo(),
		mrm,
	)
	if err := mmManager.Start(); err != nil {
		return fmt.Errorf("Error starting mm manager: %s\n", err)
	}

	sysconfigManager := sysconfig.NewManager(
		pct.NewLogger(logChan, "sysconfig"),
		sysconfigMonitor.NewFactory(logChan, itManager.Repo()),
		clock,
		dataManager.Spooler(),
		itManager.Repo(),
	)
	if err := sysconfigManager.Start(); err != nil {
		return fmt.Errorf("Error starting sysconfig manager: %s\n", err)
	}

	/**
	 * Query service
	 */
	explainService := queryService.NewExplain(
		pct.NewLogger(logChan, "query-explain"),
		&mysql.RealConnectionFactory{},
		itManager.Repo(),
	)
	queryManager := query.NewManager(
		pct.NewLogger(logChan, "query"),
		explainService,
	)
	if err := queryManager.Start(); err != nil {
		return fmt.Errorf("Error starting query manager: %s\n", err)
	}

	/**
	 * Query Analytics
	 */

	qanManager := qan.NewManager(
		pct.NewLogger(logChan, "qan"),
		clock,
		itManager.Repo(),
		mrm,
		connFactory,
		qanFactory.NewRealAnalyzerFactory(
			logChan,
			qanFactory.NewRealIntervalIterFactory(logChan),
			slowlog.NewRealWorkerFactory(logChan),
			perfschema.NewRealWorkerFactory(logChan),
			dataManager.Spooler(),
			clock,
		),
	)

	if err := qanManager.Start(); err != nil {
		return fmt.Errorf("Error starting qan manager: %s\n", err)
	}

	/**
	 * Sysinfo
	 */
	sysinfoManager := sysinfo.NewManager(
		pct.NewLogger(logChan, "sysinfo"),
	)

	// MySQL Sysinfo
	mysqlSysinfoService := mysqlSysinfo.NewMySQL(
		pct.NewLogger(logChan, "sysinfo-mysql"),
		itManager.Repo(),
	)
	if err := sysinfoManager.RegisterService("MySQLSummary", mysqlSysinfoService); err != nil {
		return fmt.Errorf("Error registering Mysql Sysinfo service: %s\n", err)
	}

	// System Sysinfo
	systemSysinfoService := systemSysinfo.NewSystem(
		pct.NewLogger(logChan, "sysinfo-system"),
	)
	if err := sysinfoManager.RegisterService("SystemSummary", systemSysinfoService); err != nil {
		return fmt.Errorf("Error registering System Sysinfo service: %s\n", err)
	}

	// Start Sysinfo manager
	if err := sysinfoManager.Start(); err != nil {
		return fmt.Errorf("Error starting Sysinfo manager: %s\n", err)
	}

	/**
	 * Signal handler
	 */

	// Generally the agent has a crash-only design, but QAN is so far the only service
	// which reconfigures MySQL: it enables the slow log, sets long_query_time, etc.
	// It's not terrible to leave slow log on, but it's nicer to turn it off.
	sigChan := make(chan os.Signal, 1)
	stopChan := make(chan error, 2)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		golog.Printf("Caught %s signal, shutting down...\n", sig)
		stopChan <- qanManager.Stop()
	}()

	/**
	 * Agent
	 */

	cmdClient, err := client.NewWebsocketClient(pct.NewLogger(logChan, "agent-ws"), api, "cmd", headers)
	if err != nil {
		golog.Fatal(err)
	}

	// The official list of services known to the agent.  Adding a new service
	// requires a manager, starting the manager as above, and adding the manager
	// to this map.
	services := map[string]pct.ServiceManager{
		"log":       logManager,
		"data":      dataManager,
		"qan":       qanManager,
		"mm":        mmManager,
		"instance":  itManager,
		"mrms":      mrmsManager,
		"sysconfig": sysconfigManager,
		"query":     queryManager,
		"sysinfo":   sysinfoManager,
	}

	// Set the global pct/cmd.Factory, used for the Restart cmd.
	pctCmd.Factory = &pctCmd.RealCmdFactory{}

	agentLogger := pct.NewLogger(logChan, "agent")

	agent := agent.NewAgent(
		agentConfig,
		agentLogger,
		api,
		cmdClient,
		services,
	)

	/**
	 * Run agent, wait for it to stop, signal, or crash.
	 */

	var stopErr error
	go func() {
		defer func() {
			if err := recover(); err != nil {
				errMsg := fmt.Sprintf("Agent crashed: %s", err)
				golog.Println(errMsg)
				agentLogger.Error(errMsg)
				stopChan <- fmt.Errorf("%s", errMsg)
			}
		}()
		stopChan <- agent.Run()
	}()

	// Wait for agent to stop, or for signals.
	agentRunning := true
	statusSigChan := make(chan os.Signal, 1)
	signal.Notify(statusSigChan, syscall.SIGUSR1) // kill -USER1 PID
	reconnectSigChan := make(chan os.Signal, 1)
	signal.Notify(reconnectSigChan, syscall.SIGHUP) // kill -HUP PID
	for agentRunning {
		select {
		case stopErr = <-stopChan: // agent or signal
			golog.Println("Agent stopped, shutting down...")
			agentLogger.Info("Agent stopped")
			agentRunning = false
		case <-statusSigChan:
			status := agent.AllStatus()
			golog.Printf("Status: %+v\n", status)
		case <-reconnectSigChan:
			u, _ := user.Current()
			cmd := &proto.Cmd{
				Ts:        time.Now().UTC(),
				User:      u.Username + " (SIGHUP)",
				AgentUuid: agentConfig.AgentUuid,
				Service:   "agent",
				Cmd:       "Reconnect",
			}
			agent.Handle(cmd)
		}
	}

	qanManager.Stop()           // see Signal handler ^
	time.Sleep(2 * time.Second) // wait for final replies and log entries
	return stopErr
}

func ConnectAPI(agentConfig *agent.Config, retry int) (*pct.API, error) {
	golog.Println("ApiHostname: " + agentConfig.ApiHostname)
	golog.Println("ApiKey: " + agentConfig.ApiKey)

	api := pct.NewAPI()
	backoff := pct.NewBackoff(5 * time.Minute)
	week := time.Hour * 24 * 7
	t0 := time.Now()
	try := 0
	for (retry == -1 || try < retry) && time.Now().Sub(t0) < week {
		try++
		time.Sleep(backoff.Wait())
		golog.Println("Connecting to API")
		if err := api.Connect(agentConfig.ApiHostname, agentConfig.ApiKey, agentConfig.AgentUuid); err != nil {
			golog.Println(err)
			continue
		}
		golog.Println("Connected to API")
		return api, nil // success
	}

	return nil, errors.New("Timeout connecting to " + agentConfig.ApiHostname)
}

func main() {
	if err := run(); err != nil {
		golog.Fatal(err) // non-zero exit
		os.Exit(1)
	}
	os.Exit(0)
}
