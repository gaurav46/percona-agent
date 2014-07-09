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

package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/percona/cloud-protocol/proto"
	"github.com/percona/percona-agent/pct"
	"time"
)

type Connector interface {
	DB() *sql.DB
	DSN() string
	Connect(tries uint) error
	Close()
	Explain(q string, db string) (explain *proto.ExplainResult, err error)
	Set([]Query) error
	GetGlobalVarString(varName string) string
	Uptime() (uptime int64)
}

type Connection struct {
	dsn     string
	conn    *sql.DB
	backoff *pct.Backoff
}

func NewConnection(dsn string) *Connection {
	c := &Connection{
		dsn:     dsn,
		backoff: pct.NewBackoff(20 * time.Second),
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
		return nil
	}

	return errors.New(fmt.Sprintf("Failed to connect to MySQL after %d tries (%s)", tries, err))
}

func (c *Connection) Close() {
	if c.conn != nil {
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

	err = c.fillCreateTableInClassicExplain(tx, classicExplain)
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
		if _, err := c.conn.Exec(query.Set); err != nil {
			return err
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

func (c *Connection) Uptime() (uptime int64) {
	if c.conn == nil {
		return 0
	}
	// Result from SHOW STATUS includes two columns,
	// Variable_name and Value, we ignore the first one as we need only Value
	var varName string
	c.conn.QueryRow("SHOW STATUS LIKE 'Uptime'").Scan(&varName, &uptime)
	return uptime
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
	// NOTE about below implementation: https://github.com/go-sql-driver/mysql/issues/253
	rows, err := tx.Query(fmt.Sprintf("EXPLAIN /*!50605 FORMAT=JSON*/ %s", query))
	if err != nil {
		return "", err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}
	isJSON := len(columns) == 1

	// If result is not a json, then json format is not supported
	// In such case we return empty string, without error
	if !isJSON {
		return "", nil
	}

	if !rows.Next() {
		return "", fmt.Errorf("Error when getting row with EXPLAIN FORMAT=JSON")
	}

	// Fetch json
	err = rows.Scan(&jsonExplain)
	if err != nil {
		return "", err
	}
	return jsonExplain, nil
}

func (c *Connection) showCreateTable(tx *sql.Tx, table string) (createTable proto.NullString, err error) {
	// Result from SHOW CREATE TABLE includes two columns,
	// "Table" and "Create Table", we ignore the first one as we need only "Create Table"
	var tableName string
	err = tx.QueryRow(fmt.Sprintf("SHOW CREATE TABLE %s", table)).Scan(&tableName, &createTable)
	if err != nil {
		return proto.NullString{}, err
	}
	return createTable, nil
}

func (c *Connection) fillCreateTableInClassicExplain(tx *sql.Tx, classicExplain []*proto.ExplainRow) (err error) {
	for _, explainRow := range classicExplain {
		tableName := explainRow.Table.String
		if isRealTable(tableName) {
			explainRow.CreateTable, err = c.showCreateTable(tx, tableName)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// http://dev.mysql.com/doc/refman/5.6/en/explain-output.html#explain_table
func isRealTable(tableName string) bool {
	// If table name is empty then this is obviously not a real table
	if tableName == "" {
		return false
	}

	// Table is not real also if it matches one of the following values:
	// *    <unionM,N>: The row refers to the union of the rows with id values of M and N.
	// *    <derivedN>: The row refers to the derived table result for the row with an id value of N.
	//                  A derived table may result, for example, from a subquery in the FROM clause.
	// *    <subqueryN>: The row refers to the result of a materialized subquery for the row with an id value of N.
	// So, for simplicity assuming that table is not real if first letter matches "<"
	if string([]rune(tableName)[0]) == "<" {
		return false
	}

	return true
}
