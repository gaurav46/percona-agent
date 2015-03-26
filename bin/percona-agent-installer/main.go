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
	"flag"
	"fmt"
	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/agent"
	"github.com/percona/percona-agent/bin/percona-agent-installer/api"
	"github.com/percona/percona-agent/bin/percona-agent-installer/installer"
	"github.com/percona/percona-agent/bin/percona-agent-installer/term"
	"github.com/percona/percona-agent/instance"
	"github.com/percona/percona-agent/pct"
	"log"
	"os"
)

const (
	DEFAULT_APP_HOSTNAME = "https://cloud.percona.com"
)

var (
	flagApiHostname             string
	flagApiKey                  string
	flagBasedir                 string
	flagDebug                   bool
	flagCreateMySQLInstance     bool
	flagCreateServerInstance    bool
	flagStartServices           bool
	flagCreateAgent             bool
	flagStartMySQLServices      bool
	flagMySQL                   bool
	flagOldPasswords            bool
	flagPlainPasswords          bool
	flagInteractive             bool
	flagMySQLDefaultsFile       string
	flagAutoDetectMySQL         bool
	flagCreateMySQLUser         bool
	flagAgentMySQLUser          string
	flagAgentMySQLPass          string
	flagMySQLUser               string
	flagMySQLPass               string
	flagMySQLHost               string
	flagMySQLPort               string
	flagMySQLSocket             string
	flagMySQLMaxUserConnections int64
)

func init() {
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)

	flag.StringVar(&flagApiHostname, "api-host", agent.DEFAULT_API_HOSTNAME, "API host")
	flag.StringVar(&flagApiKey, "api-key", "", "API key, it is available at "+DEFAULT_APP_HOSTNAME+"/api-key")
	flag.StringVar(&flagBasedir, "basedir", pct.DEFAULT_BASEDIR, "Agent basedir")
	flag.BoolVar(&flagDebug, "debug", false, "Debug")
	// --
	flag.BoolVar(&flagMySQL, "mysql", true, "Install for MySQL")
	flag.BoolVar(&flagCreateMySQLInstance, "create-mysql-instance", true, "Create MySQL instance")
	flag.BoolVar(&flagCreateServerInstance, "create-server-instance", true, "Create server instance")
	flag.BoolVar(&flagStartServices, "start-services", true, "Start all services")
	flag.BoolVar(&flagStartMySQLServices, "start-mysql-services", true, "Start MySQL services")
	flag.BoolVar(&flagCreateAgent, "create-agent", true, "Create agent")
	flag.BoolVar(&flagOldPasswords, "old-passwords", false, "Old passwords")
	flag.BoolVar(&flagPlainPasswords, "plain-passwords", false, "Plain passwords") // @todo: Workaround used in tests for "stty: standard input: Inappropriate ioctl for device"
	flag.BoolVar(&flagInteractive, "interactive", true, "Prompt for input on STDIN")
	flag.BoolVar(&flagAutoDetectMySQL, "auto-detect-mysql", true, "Auto detect MySQL options")
	flag.BoolVar(&flagCreateMySQLUser, "create-mysql-user", true, "Create MySQL user for agent")
	flag.StringVar(&flagAgentMySQLUser, "agent-mysql-user", "", "MySQL username for agent")
	flag.StringVar(&flagAgentMySQLPass, "agent-mysql-pass", "", "MySQL password for agent")
	flag.StringVar(&flagMySQLDefaultsFile, "mysql-defaults-file", "", "Path to my.cnf, used for auto detection of connection details")
	flag.StringVar(&flagMySQLUser, "mysql-user", "", "MySQL username")
	flag.StringVar(&flagMySQLPass, "mysql-pass", "", "MySQL password")
	flag.StringVar(&flagMySQLHost, "mysql-host", "", "MySQL host")
	flag.StringVar(&flagMySQLPort, "mysql-port", "", "MySQL port")
	flag.StringVar(&flagMySQLSocket, "mysql-socket", "", "MySQL socket file")
	flag.Int64Var(&flagMySQLMaxUserConnections, "mysql-max-user-connections", 5, "Max number of MySQL connections")
}

func main() {
	// It flag is unknown it exist with os.Exit(10),
	// so exit code=10 is strictly reserved for flags
	// Don't use it anywhere else, as shell script install.sh depends on it
	// NOTE: standard flag.Parse() was using os.Exit(2)
	//       which was the same as returned with ctrl+c
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		os.Exit(10)
	}

	agentConfig := &agent.Config{
		ApiHostname: flagApiHostname,
		ApiKey:      flagApiKey,
	}
	// todo: do flags a better way
	if !flagMySQL {
		flagCreateMySQLInstance = false
		flagStartMySQLServices = false
	}

	if flagMySQLSocket != "" && flagMySQLHost != "" {
		log.Println("Options -mysql-socket and -mysql-host are exclusive\n")
		os.Exit(1)
	}

	if flagMySQLSocket != "" && flagMySQLPort != "" {
		log.Println("Options -mysql-socket and -mysql-port are exclusive\n")
		os.Exit(1)
	}

	flags := installer.Flags{
		Bool: map[string]bool{
			"debug":                  flagDebug,
			"create-server-instance": flagCreateServerInstance,
			"start-services":         flagStartServices,
			"create-mysql-instance":  flagCreateMySQLInstance,
			"start-mysql-services":   flagStartMySQLServices,
			"create-agent":           flagCreateAgent,
			"old-passwords":          flagOldPasswords,
			"plain-passwords":        flagPlainPasswords,
			"interactive":            flagInteractive,
			"auto-detect-mysql":      flagAutoDetectMySQL,
			"create-mysql-user":      flagCreateMySQLUser,
			"mysql":                  flagMySQL,
		},
		String: map[string]string{
			"app-host":            DEFAULT_APP_HOSTNAME,
			"mysql-defaults-file": flagMySQLDefaultsFile,
			"agent-mysql-user":    flagAgentMySQLUser,
			"agent-mysql-pass":    flagAgentMySQLPass,
			"mysql-user":          flagMySQLUser,
			"mysql-pass":          flagMySQLPass,
			"mysql-host":          flagMySQLHost,
			"mysql-port":          flagMySQLPort,
			"mysql-socket":        flagMySQLSocket,
		},
		Int64: map[string]int64{
			"mysql-max-user-connections": flagMySQLMaxUserConnections,
		},
	}

	// Agent stores all its files in the basedir.  This must be called first
	// because installer uses pct.Basedir and assumes it's already initialized.
	if err := pct.Basedir.Init(flagBasedir); err != nil {
		log.Printf("Error initializing basedir %s: %s\n", flagBasedir, err)
		os.Exit(1)
	}

	apiConnector := pct.NewAPI()
	api := api.New(apiConnector, flagDebug)
	logChan := make(chan *proto.LogEntry, 100)
	logger := pct.NewLogger(logChan, "instance-repo")
	instanceRepo := instance.NewRepo(logger, pct.Basedir.Dir("config"), apiConnector)
	terminal := term.NewTerminal(os.Stdin, flagInteractive, flagDebug)
	agentInstaller := installer.NewInstaller(terminal, flagBasedir, api, instanceRepo, agentConfig, flags)
	fmt.Println("CTRL-C at any time to quit")
	// todo: catch SIGINT and clean up
	if err := agentInstaller.Run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	os.Exit(0)
}
