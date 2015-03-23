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

import (
	"time"

	"github.com/percona/cloud-protocol/proto"
)

/**
 * A Monitor collects one or more Metric, usually many.  The MySQL monitor
 * (mysql/monitor.go) collects most SHOW STATUS variables, each as its own
 * Metric.  Each Metric collected during a single period are sent as a
 * Collection to an Aggregator (aggregator.go).  The Aggregator keeps Stats
 * for each unique Metric, for each service instance.  When it's time to
 * report, the per-instance stats are summarized and the stats are encoded
 * in a Report and sent to the Spooler (data/spooler.go).
 */

// Collect metrics when tickChan ticks, send to collecitonChan.
type Monitor interface {
	Start(tickChan chan time.Time, collectionChan chan *Collection) error
	Stop() error
	Status() map[string]string
	TickChan() chan time.Time
	Config() interface{}
}

type MonitorFactory interface {
	Make(service string, instanceId string, data []byte) (Monitor, error)
}

var MetricTypes map[string]bool = map[string]bool{
	"gauge":   true,
	"counter": true,
}

// A single metric and its value at any time.  Monitors are responsible for
// getting these and sending them as a Collection to an aggregator.
type Metric struct {
	Name   string // mysql/status/Threads_running
	Type   string // gauge, counter, string
	Number float64
	String string
}

// All metrics from a service instance collected at the same time.
// Collections can come from different instances.  For example,
// one agent can monitor two different MySQL instances.
type Collection struct {
	proto.ServiceInstance
	Ts      int64 // UTC Unix timestamp
	Metrics []Metric
}

// Stats for each metric from a service instance, computed at each report interval.
type InstanceStats struct {
	proto.ServiceInstance
	Stats map[string]*Stats // keyed on metric name
}

type Report struct {
	Ts       time.Time // start, UTC
	Duration uint      // seconds
	Stats    []*InstanceStats
}
