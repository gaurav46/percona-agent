package qan

type Config struct {
	Interval          uint    // minutes, "How often to report"
	LongQueryTime     float64 // >= 0, microsecond precision
	MaxSlowLogSize    uint64  // bytes, 0 = no max
	RemoveOldSlowLogs bool    // only if MaxSlowLogSize > 0
	ExampleQueries    bool    // only fingerprints if false
	MysqlDsn          string
	MaxWorkers        int
	WorkerRuntime     uint
}
