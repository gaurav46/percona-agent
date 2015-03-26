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

package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/hashicorp/go-version"
	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/pct"
	"regexp"
)

type Query struct {
	Set    string // SET GLOBAL long_query_time=0
	Verify string // SELECT @@long_query_time
	Expect string // 0
}

type Connector interface {
	DB() *sql.DB
	DSN() string
	Connect(tries uint) error
	Close()
	Explain(q string, db string) (explain *proto.ExplainResult, err error)
	Set([]Query) error
	GetGlobalVarString(varName string) string
	GetGlobalVarNumber(varName string) float64
	Uptime() (uptime int64, err error)
	AtLeastVersion(v string) (bool, error)
}

type Connection struct {
	dsn             string
	conn            *sql.DB
	backoff         *pct.Backoff
	connectedAmount uint
	connectionMux   *sync.Mutex
}

func NewConnection(dsn string) *Connection {
	c := &Connection{
		dsn:           dsn,
		backoff:       pct.NewBackoff(20 * time.Second),
		connectionMux: new(sync.Mutex),
	}
	return c
}

func (c *Connection) DB() *sql.DB {
	return c.conn
}

func (c *Connection) DSN() string {
	return c.dsn
}

func (c *Connection) Connect(tries uint) error {
	if tries == 0 {
		return nil
	}
	c.connectionMux.Lock()
	defer c.connectionMux.Unlock()
	if c.connectedAmount > 0 {
		// already have opened connection
		c.connectedAmount++
		return nil
	}
	var err error
	var db *sql.DB
	for i := tries; i > 0; i-- {
		// Wait before attempt.
		time.Sleep(c.backoff.Wait())

		// Open connection to MySQL but...
		db, err = sql.Open("mysql", c.dsn)
		if err != nil {
			continue
		}

		// ...try to use the connection for real.
		if err = db.Ping(); err != nil {
			// Connection failed.  Wrong username or password?
			db.Close()
			continue
		}

		// Connected
		c.conn = db
		c.backoff.Success()
		c.connectedAmount++
		return nil
	}

	return fmt.Errorf("Cannot connect to MySQL %s: %s", HideDSNPassword(c.dsn), FormatError(err))
}

func (c *Connection) Close() {
	c.connectionMux.Lock()
	defer c.connectionMux.Unlock()
	if c.connectedAmount == 0 {
		// connection closed already
		return
	}
	c.connectedAmount--
	if c.connectedAmount == 0 && c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

func (c *Connection) Explain(query string, db string) (explain *proto.ExplainResult, err error) {
	// Transaction because we need to ensure USE and EXPLAIN are run in one connection
	tx, err := c.conn.Begin()
	defer tx.Rollback()
	if err != nil {
		return nil, err
	}

	// Some queries are not bound to database
	if db != "" {
		_, err := tx.Exec(fmt.Sprintf("USE %s", db))
		if err != nil {
			return nil, err
		}
	}

	classicExplain, err := c.classicExplain(tx, query)
	if err != nil {
		return nil, err
	}

	jsonExplain, err := c.jsonExplain(tx, query)
	if err != nil {
		return nil, err
	}

	explain = &proto.ExplainResult{
		Classic: classicExplain,
		JSON:    jsonExplain,
	}

	return explain, nil
}

func (c *Connection) Set(queries []Query) error {
	if c.conn == nil {
		return errors.New("Not connected")
	}
	for _, query := range queries {
		if query.Set != "" {
			if _, err := c.conn.Exec(query.Set); err != nil {
				return err
			}
		}
		if query.Verify != "" {
			got := c.GetGlobalVarString(query.Verify)
			if got != query.Expect {
				return fmt.Errorf(
					"Global variable '%s' is set to '%s' but needs to be '%s'. "+
						"Consult the MySQL manual, or contact Percona Support, "+
						"for help configuring this variable, then try again.",
					query.Verify, got, query.Expect)
			}
		}
	}
	return nil
}

func (c *Connection) GetGlobalVarString(varName string) string {
	if c.conn == nil {
		return ""
	}
	var varValue string
	c.conn.QueryRow("SELECT @@GLOBAL." + varName).Scan(&varValue)
	return varValue
}

func (c *Connection) GetGlobalVarNumber(varName string) float64 {
	if c.conn == nil {
		return 0
	}
	var varValue float64
	c.conn.QueryRow("SELECT @@GLOBAL." + varName).Scan(&varValue)
	return varValue
}

func (c *Connection) AtLeastVersion(v string) (bool, error) {
	mysqlVersion := c.GetGlobalVarString("version") // Version in the form m.n.o-ubuntu
	return AtLeastVersion(mysqlVersion, v)
}

// Check if version v2 is equal or higher than v1 (v2 >= v1)
// v2 can be in form m.n.o-ubuntu
func AtLeastVersion(v1, v2 string) (bool, error) {
	re := regexp.MustCompile("-.*$")
	v1 = re.ReplaceAllString(v1, "") // Strip everything after the first dash

	v, err := version.NewVersion(v1)
	if err != nil {
		return false, err
	}
	constraints, err := version.NewConstraint(">= " + v2)
	if err != nil {
		return false, err
	}
	if constraints.Check(v) {
		return true, nil
	}
	return false, nil
}

func (c *Connection) Uptime() (uptime int64, err error) {
	if c.conn == nil {
		return 0, fmt.Errorf("Error while getting Uptime(). Not connected to the db: %s", c.DSN())
	}
	// Result from SHOW STATUS includes two columns,
	// Variable_name and Value, we ignore the first one as we need only Value
	var varName string
	c.conn.QueryRow("SHOW STATUS LIKE 'Uptime'").Scan(&varName, &uptime)
	return uptime, nil
}

func (c *Connection) classicExplain(tx *sql.Tx, query string) (classicExplain []*proto.ExplainRow, err error) {
	// Partitions are introduced since MySQL 5.1
	// We can simply run EXPLAIN /*!50100 PARTITIONS*/ to get this column when it's available
	// without prior check for MySQL version.
	rows, err := tx.Query(fmt.Sprintf("EXPLAIN /*!50100 PARTITIONS*/ %s", query))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Go rows.Scan() expects exact number of columns
	// so when number of columns is undefined then the easiest way to
	// overcome this problem is to count received number of columns
	// With 'partitions' it is 11 columns
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	hasPartitions := len(columns) == 11

	for rows.Next() {
		explainRow := &proto.ExplainRow{}
		if hasPartitions {
			err = rows.Scan(
				&explainRow.Id,
				&explainRow.SelectType,
				&explainRow.Table,
				&explainRow.Partitions, // Since MySQL 5.1
				&explainRow.Type,
				&explainRow.PossibleKeys,
				&explainRow.Key,
				&explainRow.KeyLen,
				&explainRow.Ref,
				&explainRow.Rows,
				&explainRow.Extra,
			)
		} else {
			err = rows.Scan(
				&explainRow.Id,
				&explainRow.SelectType,
				&explainRow.Table,
				&explainRow.Type,
				&explainRow.PossibleKeys,
				&explainRow.Key,
				&explainRow.KeyLen,
				&explainRow.Ref,
				&explainRow.Rows,
				&explainRow.Extra,
			)
		}
		if err != nil {
			return nil, err
		}
		classicExplain = append(classicExplain, explainRow)
	}
	err = rows.Err()
	if err != nil {
		return nil, err
	}

	return classicExplain, nil
}

func (c *Connection) jsonExplain(tx *sql.Tx, query string) (jsonExplain string, err error) {
	// EXPLAIN in JSON format is introduced since MySQL 5.6.5
	jsonFormatSupported, err := c.AtLeastVersion("5.6.5")
	if err != nil {
		return "", err
	}
	if !jsonFormatSupported {
		return "", nil
	}

	err = tx.QueryRow(fmt.Sprintf("EXPLAIN FORMAT=JSON %s", query)).Scan(&jsonExplain)
	if err != nil {
		return "", err
	}

	return jsonExplain, nil
}
