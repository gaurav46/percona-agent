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

package installer

import (
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/agent"
	"github.com/percona/percona-agent/bin/percona-agent-installer/api"
	"github.com/percona/percona-agent/bin/percona-agent-installer/term"
	"github.com/percona/percona-agent/instance"
	"github.com/percona/percona-agent/mysql"
	"log"
	"net"
	"net/url"
	"os"
	"regexp"
	"time"
)

var portNumberRe = regexp.MustCompile(`\.\d+$`)

type Flags struct {
	Bool   map[string]bool
	String map[string]string
	Int64  map[string]int64
}

type Installer struct {
	term         *term.Terminal
	basedir      string
	api          *api.Api
	instanceRepo *instance.Repo
	agentConfig  *agent.Config
	flags        Flags
	// --
	hostname   string
	defaultDSN mysql.DSN
}

func NewInstaller(terminal *term.Terminal, basedir string, api *api.Api, instanceRepo *instance.Repo, agentConfig *agent.Config, flags Flags) *Installer {
	if agentConfig.ApiHostname == "" {
		agentConfig.ApiHostname = agent.DEFAULT_API_HOSTNAME
	}
	if agentConfig.PidFile == "" {
		agentConfig.PidFile = agent.DEFAULT_PIDFILE
	}
	hostname, _ := os.Hostname()
	defaultDSN := mysql.DSN{
		Username: flags.String["mysql-user"],
		Password: flags.String["mysql-pass"],
		Hostname: flags.String["mysql-host"],
		Port:     flags.String["mysql-port"],
		Socket:   flags.String["mysql-socket"],
	}
	installer := &Installer{
		term:         terminal,
		basedir:      basedir,
		api:          api,
		instanceRepo: instanceRepo,
		agentConfig:  agentConfig,
		flags:        flags,
		// --
		hostname:   hostname,
		defaultDSN: defaultDSN,
	}
	return installer
}

func (i *Installer) Run() (err error) {
	/**
	 * Get the API key.
	 */
	err = i.InstallerGetApiKey()
	if err != nil {
		return err
	}

	/**
	 * Verify the API key by pinging the API.
	 */
	err = i.VerifyApiKey()
	if err != nil {
		return err
	}

	if i.flags.Bool["create-agent"] {
		protoAgent, err := i.InstallerCreateAgentWithInitialServiceConfigs()
		if err != nil {
			return err
		}

		// To save data we need agent config with uuid and links
		i.agentConfig.AgentUuid = protoAgent.Uuid
		i.agentConfig.Links = protoAgent.Links
	}

	/**
	 * Create new service instances.
	 */

	// Server instance
	si, err := i.InstallerCreateServerInstance()
	if err != nil {
		return err
	}

	// MySQL instance
	var mi *proto.MySQLInstance
	if i.flags.Bool["mysql"] {
		mi, err = i.InstallerCreateMySQLInstance()
		if err != nil {
			if i.flags.Bool["interactive"] {
				return err
			} else {
				// Automated install, log the error and continue.
				fmt.Printf("Failed to set up MySQL (ignoring because interactive=false): %s\n", err)
			}
		}
	}

	if err = i.writeInstances(si, mi); err != nil {
		return fmt.Errorf("Created agent but failed to write service instances: %s", err)
	}

	/**
	 * Create agent with initial service configs.
	 */
	if i.flags.Bool["create-agent"] {
		/**
		 * Get default configs for all services.
		 */
		configs, err := i.InstallerGetDefaultConfigs(si, mi)
		if err != nil {
			return err
		}

		// Save configs
		if err := i.writeConfigs(configs); err != nil {
			return fmt.Errorf("Created agent but failed to write configs: %s", err)
		}
	} else {
		fmt.Println("Not creating agent (-create-agent=false)")
	}

	return nil // success
}

func (i *Installer) InstallerGetApiKey() error {
	fmt.Printf("API host: %s\n", i.agentConfig.ApiHostname)

	if !i.flags.Bool["interactive"] && i.agentConfig.ApiKey == "" {
		return fmt.Errorf(
			"API key is required, please provide it with -api-key option.\n" +
				"API Key is available at " + i.flags.String["app-host"] + "/api-key",
		)
	} else {
		if i.agentConfig.ApiKey == "" {
			fmt.Printf(
				"No API Key Defined.\n" +
					"Please Enter your API Key, it is available at https://cloud.percona.com/api-key\n",
			)
		}
		for i.agentConfig.ApiKey == "" {
			apiKey, err := i.term.PromptString("API key", "")
			if err != nil {
				return err
			}
			if apiKey == "" {
				fmt.Println("API key is required, please try again.")
				continue
			}
			i.agentConfig.ApiKey = apiKey
			break
		}
	}

	return nil
}

func (i *Installer) VerifyApiKey() error {
VERIFY_API_KEY:
	for {
		startTime := time.Now()
		fmt.Printf("Verifying API key %s...\n", i.agentConfig.ApiKey)
		headers := map[string]string{
			"X-Percona-Agent-Version": agent.VERSION,
		}
		code, err := i.api.Init(i.agentConfig.ApiHostname, i.agentConfig.ApiKey, headers)
		elapsedTime := time.Since(startTime)
		elapsedTimeInSeconds := elapsedTime / time.Second

		timeout := false
		if urlErr, ok := err.(*url.Error); ok {
			if netOpErr, ok := urlErr.Err.(*net.OpError); ok && netOpErr.Timeout() {
				timeout = true
			}
		}
		if i.flags.Bool["debug"] {
			log.Printf("code=%d\n", code)
			log.Printf("err=%s\n", err)
		}
		ok := false
		if timeout {
			fmt.Printf(
				"Error: API connection timeout (%ds): %s\n"+
					"Before you try again, please check your connection and DNS configuration.\n",
				elapsedTimeInSeconds,
				err,
			)
		} else if err != nil {
			fmt.Printf("Error: %s\n", err)
		} else if code >= 500 {
			fmt.Printf("Sorry, there's an API problem (status code %d). "+
				"Please try to install again. If the problem continues, contact Percona.\n",
				code)
		} else if code == 401 {
			return fmt.Errorf("Access denied.  Check the API key and try again.")
		} else if code >= 300 {
			fmt.Printf("Sorry, there's an installer problem (status code %d). "+
				"Please try to install again. If the problem continues, contact Percona.\n",
				code)
		} else if code != 200 {
			fmt.Printf("Sorry, there's an installer problem (status code %d). "+
				"Please try to install again. If the problem continues, contact Percona.\n",
				code)
		} else {
			ok = true
		}

		if !ok {
			again, err := i.term.PromptBool("Try again?", "Y")
			if err != nil {
				return err
			}
			if !again {
				return fmt.Errorf("Failed to verify API key")
			}
			continue VERIFY_API_KEY
		}

		// https://jira.percona.com/browse/PCT-617
		// Warn user if request took at least 5s
		if elapsedTimeInSeconds >= 5 {
			fmt.Printf(
				"WARNING: Request to API took %d seconds but it should have taken < 1 second."+
					" There might be a connection problem, or resolving DNS is very slow."+
					" Before continuing, please check the connection and DNS configuration"+
					" as this could prevent percona-agent from installing or working properly."+
					" If running CentOS or Fedora 19+ in a Vagrant VirtualBox, see this bug:\n"+
					" https://github.com/mitchellh/vagrant/issues/1172\n",
				elapsedTimeInSeconds,
			)
			proceed, err := i.term.PromptBool("Continue?", "Y")
			if err != nil {
				return err
			}
			if !proceed {
				return fmt.Errorf("Failed because of slow connection")
			}
		}

		break
	}

	return nil
}

func (i *Installer) InstallerCreateServerInstance() (si *proto.ServerInstance, err error) {
	if i.flags.Bool["create-server-instance"] {
		// POST <api>/instances/server
		si = &proto.ServerInstance{
			Hostname: i.hostname,
		}
		si, err = i.api.CreateServerInstance(si)
		if err != nil {
			return nil, err
		}
		fmt.Printf("Created server instance: hostname=%s id=%d\n", si.Hostname, si.Id)
	} else {
		fmt.Println("Not creating server instance (-create-server-instance=false)")
	}

	return si, nil
}

func (i *Installer) InstallerCreateMySQLInstance() (mi *proto.MySQLInstance, err error) {
	if i.flags.Bool["create-mysql-instance"] {
		// Get MySQL DSN for agent to use.
		// It is new MySQL user created just for agent
		// or user is asked for existing one.
		// DSN is verified prior returning by connecting to MySQL.
		agentDSN, err := i.getAgentDSN()
		if err != nil {
			return nil, err
		}

		// Create MySQL instance in API.
		dsnString, _ := agentDSN.DSN()
		mi = &proto.MySQLInstance{
			Hostname: i.hostname,
			DSN:      dsnString,
		}
		mi, err = i.api.CreateMySQLInstance(mi)
		if err != nil {
			return nil, err
		}
		fmt.Printf("Created MySQL instance: dsn=%s hostname=%s id=%d\n", mi.DSN, mi.Hostname, mi.Id)
	} else {
		fmt.Println("Not creating MySQL instance (-create-mysql-instance=false)")
	}

	return mi, nil
}

func (i *Installer) InstallerGetDefaultConfigs(si *proto.ServerInstance, mi *proto.MySQLInstance) (configs []proto.AgentConfig, err error) {
	agentConfig, err := i.getAgentConfig()
	if err != nil {
		return nil, err
	}
	configs = append(configs, *agentConfig)

	// Log Config
	logConfig, err := i.getLogConfig()
	if err != nil {
		return nil, err
	}
	configs = append(configs, *logConfig)

	// Data Config
	dataConfig, err := i.getDataConfig()
	if err != nil {
		return nil, err
	}
	configs = append(configs, *dataConfig)

	if i.flags.Bool["start-services"] {
		// Server metrics monitor
		config, err := i.api.GetMmServerConfig(si)
		if err != nil {
			fmt.Println(err)
			fmt.Println("WARNING: cannot start server metrics monitor")
		} else {
			configs = append(configs, *config)
		}

		if i.flags.Bool["start-mysql-services"] {
			if mi != nil {
				// MySQL metrics tracker
				config, err = i.api.GetMmMySQLConfig(mi)
				if err != nil {
					fmt.Println(err)
					fmt.Println("WARNING: cannot start MySQL metrics monitor")
				} else {
					configs = append(configs, *config)
				}

				// MySQL config tracker
				config, err = i.api.GetSysconfigMySQLConfig(mi)
				if err != nil {
					fmt.Println(err)
					fmt.Println("WARNING: cannot start MySQL configuration monitor")
				} else {
					configs = append(configs, *config)
				}

				// QAN
				// MySQL is local if the server hostname == MySQL hostname without port number.
				if i.hostname == portNumberRe.ReplaceAllLiteralString(mi.Hostname, "") {
					if i.flags.Bool["debug"] {
						log.Printf("MySQL is local")
					}
					config, err := i.api.GetQanConfig(mi)
					if err != nil {
						fmt.Println(err)
						fmt.Println("WARNING: cannot start Query Analytics")
					} else {
						configs = append(configs, *config)
					}
				}
			}
		} else {
			fmt.Println("Not starting MySQL services (-start-mysql-services=false)")
		}
	} else {
		fmt.Println("Not starting default services (-start-services=false)")
	}

	return configs, nil
}

func (i *Installer) InstallerCreateAgentWithInitialServiceConfigs() (protoAgent *proto.Agent, err error) {
	protoAgent = &proto.Agent{
		Hostname: i.hostname,
		Version:  agent.VERSION,
	}
	protoAgent, err = i.api.CreateAgent(protoAgent)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Created agent: uuid=%s\n", protoAgent.Uuid)
	return protoAgent, nil
}
