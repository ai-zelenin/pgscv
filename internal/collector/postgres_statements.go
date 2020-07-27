package collector

import (
	"bytes"
	"fmt"
	"github.com/barcodepro/pgscv/internal/log"
	"github.com/barcodepro/pgscv/internal/store"
	"github.com/jackc/pgx/v4"
	"github.com/prometheus/client_golang/prometheus"
	"strconv"
	"strings"
	"text/template"
)

const (
	// postgresStatementsQueryTemplate defines template for qurying statements metrics.
	// 1. depending on user-requested AllowTrackSensitive, request or skip queries texts.
	// 2. use nullif(value, 0) to nullify zero values, NULL are skipped by stats method and metrics wil not be generated.
	postgresStatementsQueryTemplate = `SELECT
    d.datname AS datname, pg_get_userbyid(p.userid) AS usename,
    p.queryid, {{if .AllowTrackSensitive }}left(regexp_replace(p.query,E'\\s+', ' ', 'g'),1024){{else}}''{{end}} AS query,
    p.calls, p.rows,
    p.total_time, p.blk_read_time, p.blk_write_time,
    nullif(p.shared_blks_hit, 0) AS shared_blks_hit, nullif(p.shared_blks_read, 0) AS shared_blks_read,
    nullif(p.shared_blks_dirtied, 0) AS shared_blks_dirtied, nullif(p.shared_blks_written, 0) AS shared_blks_written,
    nullif(p.local_blks_hit, 0) AS local_blks_hit, nullif(p.local_blks_read, 0) AS local_blks_read,
    nullif(p.local_blks_dirtied, 0) AS local_blks_dirtied, nullif(p.local_blks_written, 0) AS local_blks_written,
    nullif(p.temp_blks_read, 0) AS temp_blks_read, nullif(p.temp_blks_written, 0) AS temp_blks_written
FROM pg_stat_statements p
JOIN pg_database d ON d.oid=p.dbid`
)

// postgresStatementsCollector ...
type postgresStatementsCollector struct {
	labelNames []string
	descs      map[string]typedDesc
}

// NewPostgresStatementsCollector returns a new Collector exposing postgres statements stats.
// For details see https://www.postgresql.org/docs/current/pgstatstatements.html
func NewPostgresStatementsCollector(constLabels prometheus.Labels) (Collector, error) {
	var labelNames = []string{"usename", "datname", "queryid", "query"}

	return &postgresStatementsCollector{
		labelNames: labelNames,
		descs: map[string]typedDesc{
			"calls": {
				desc: prometheus.NewDesc(
					prometheus.BuildFQName("postgres", "statements", "calls_total"),
					"Total number of times query has been executed.",
					labelNames, constLabels,
				), valueType: prometheus.CounterValue,
			},
			"rows": {
				desc: prometheus.NewDesc(
					prometheus.BuildFQName("postgres", "statements", "rows_total"),
					"Total number of rows retrieved or affected by the statement.",
					labelNames, constLabels,
				), valueType: prometheus.CounterValue,
			},
			"time": {
				desc: prometheus.NewDesc(
					prometheus.BuildFQName("postgres", "statements", "time_total"),
					"Total time spent in the statement in each mode, in seconds.",
					append(labelNames, "mode"), constLabels,
				), valueType: prometheus.CounterValue, factor: .001,
			},
			"blocks": {
				desc: prometheus.NewDesc(
					prometheus.BuildFQName("postgres", "statements", "blocks_total"),
					"Total number of block processed by the statement in each mode.",
					append(labelNames, "type", "access"), constLabels,
				), valueType: prometheus.CounterValue,
			},
		},
	}, nil
}

// Update method collects statistics, parse it and produces metrics that are sent to Prometheus.
func (c *postgresStatementsCollector) Update(config Config, ch chan<- prometheus.Metric) error {
	// nothing to do, pg_stat_statements not found in shared_preload_libraries
	if !config.PgStatStatements {
		return nil
	}

	// looking for source database where pg_stat_statements is installed
	conn, err := NewDBWithPgStatStatements(&config)
	if err != nil {
		return err
	}

	tmpl, err := template.New("query").Parse(postgresStatementsQueryTemplate)
	if err != nil {
		return err
	}

	params := struct{ AllowTrackSensitive bool }{AllowTrackSensitive: config.AllowTrackSensitive}
	var buf bytes.Buffer
	if err = tmpl.Execute(&buf, params); err != nil {
		return err
	}

	// get pg_stat_statements stats
	res, err := conn.GetStats(buf.String())
	if err != nil {
		return err
	}

	conn.Close()

	// parse pg_stat_statements stats
	stats := parsePostgresStatementsStats(res, c.labelNames)

	for _, stat := range stats {
		for name, desc := range c.descs {
			switch name {
			case "calls":
				ch <- desc.mustNewConstMetric(stat.calls, stat.datname, stat.usename, stat.queryid, stat.query)
			case "rows":
				ch <- desc.mustNewConstMetric(stat.rows, stat.datname, stat.usename, stat.queryid, stat.query)
			case "time":
				ch <- desc.mustNewConstMetric(stat.totalTime, stat.datname, stat.usename, stat.queryid, stat.query, "total")
				// avoid metrics spamming and send metrics only if they greater than zero.
				if stat.blkReadTime > 0 {
					ch <- desc.mustNewConstMetric(stat.blkReadTime, stat.datname, stat.usename, stat.queryid, stat.query, "ioread")
				}
				if stat.blkWriteTime > 0 {
					ch <- desc.mustNewConstMetric(stat.blkWriteTime, stat.datname, stat.usename, stat.queryid, stat.query, "iowrite")
				}
			case "blocks":
				// avoid metrics spamming and send metrics only if they greater than zero.
				if stat.sharedBlksHit > 0 {
					ch <- desc.mustNewConstMetric(stat.sharedBlksHit, stat.datname, stat.usename, stat.queryid, stat.query, "shared", "hit")
				}
				if stat.sharedBlksRead > 0 {
					ch <- desc.mustNewConstMetric(stat.sharedBlksRead, stat.datname, stat.usename, stat.queryid, stat.query, "shared", "read")
				}
				if stat.sharedBlksDirtied > 0 {
					ch <- desc.mustNewConstMetric(stat.sharedBlksDirtied, stat.datname, stat.usename, stat.queryid, stat.query, "shared", "dirtied")
				}
				if stat.sharedBlksWritten > 0 {
					ch <- desc.mustNewConstMetric(stat.sharedBlksWritten, stat.datname, stat.usename, stat.queryid, stat.query, "shared", "written")
				}
				if stat.localBlksHit > 0 {
					ch <- desc.mustNewConstMetric(stat.localBlksHit, stat.datname, stat.usename, stat.queryid, stat.query, "local", "hit")
				}
				if stat.localBlksRead > 0 {
					ch <- desc.mustNewConstMetric(stat.localBlksRead, stat.datname, stat.usename, stat.queryid, stat.query, "local", "read")
				}
				if stat.localBlksDirtied > 0 {
					ch <- desc.mustNewConstMetric(stat.localBlksDirtied, stat.datname, stat.usename, stat.queryid, stat.query, "local", "dirtied")
				}
				if stat.localBlksWritten > 0 {
					ch <- desc.mustNewConstMetric(stat.localBlksWritten, stat.datname, stat.usename, stat.queryid, stat.query, "local", "written")
				}
				if stat.tempBlksRead > 0 {
					ch <- desc.mustNewConstMetric(stat.tempBlksRead, stat.datname, stat.usename, stat.queryid, stat.query, "temp", "read")
				}
				if stat.tempBlksWritten > 0 {
					ch <- desc.mustNewConstMetric(stat.tempBlksWritten, stat.datname, stat.usename, stat.queryid, stat.query, "temp", "written")
				}
			}
		}
	}

	return nil
}

func parsePostgresStatementsStats(r *store.QueryResult, labelNames []string) map[string]postgresStatementsStat {
	var stats = make(map[string]postgresStatementsStat)

	// process row by row - on every row construct 'statement' using datname/usename/queryid trio. Next process other row's
	// fields and collect stats for constructed 'statement'.
	for _, row := range r.Rows {
		stat := postgresStatementsStat{}

		// collect label values
		for i, colname := range r.Colnames {
			switch string(colname.Name) {
			case "datname":
				stat.datname = row[i].String
			case "usename":
				stat.usename = row[i].String
			case "queryid":
				stat.queryid = row[i].String
			case "query":
				stat.query = row[i].String
			}
		}

		// Create a pool name consisting of trio database/user/queryid
		statement := strings.Join([]string{stat.datname, stat.usename, stat.queryid}, "/")

		// Put stats with labels (but with no data values yet) into stats store.
		stats[statement] = stat

		// fetch data values from columns
		for i, colname := range r.Colnames {
			// skip columns if its value used as a label
			if stringsContains(labelNames, string(colname.Name)) {
				log.Debug("skip label mapped column")
				continue
			}

			// Skip empty (NULL) values.
			if row[i].String == "" {
				log.Debug("got empty (NULL) value, skip")
				continue
			}

			// Get data value and convert it to float64 used by Prometheus.
			v, err := strconv.ParseFloat(row[i].String, 64)
			if err != nil {
				log.Errorf("skip collecting metric: %s", err)
				continue
			}

			// Run column-specific logic
			switch string(colname.Name) {
			case "calls":
				s := stats[statement]
				s.calls = v
				stats[statement] = s
			case "rows":
				s := stats[statement]
				s.rows = v
				stats[statement] = s
			case "total_time":
				s := stats[statement]
				s.totalTime = v
				stats[statement] = s
			case "blk_read_time":
				s := stats[statement]
				s.blkReadTime = v
				stats[statement] = s
			case "blk_write_time":
				s := stats[statement]
				s.blkWriteTime = v
				stats[statement] = s
			case "shared_blks_hit":
				s := stats[statement]
				s.sharedBlksHit = v
				stats[statement] = s
			case "shared_blks_read":
				s := stats[statement]
				s.sharedBlksRead = v
				stats[statement] = s
			case "shared_blks_dirtied":
				s := stats[statement]
				s.sharedBlksDirtied = v
				stats[statement] = s
			case "shared_blks_written":
				s := stats[statement]
				s.sharedBlksWritten = v
				stats[statement] = s
			case "local_blks_hit":
				s := stats[statement]
				s.localBlksHit = v
				stats[statement] = s
			case "local_blks_read":
				s := stats[statement]
				s.localBlksRead = v
				stats[statement] = s
			case "local_blks_dirtied":
				s := stats[statement]
				s.localBlksDirtied = v
				stats[statement] = s
			case "local_blks_written":
				s := stats[statement]
				s.localBlksWritten = v
				stats[statement] = s
			case "temp_blks_read":
				s := stats[statement]
				s.tempBlksRead = v
				stats[statement] = s
			case "temp_blks_written":
				s := stats[statement]
				s.tempBlksWritten = v
				stats[statement] = s
			default:
				log.Debugf("unsupported pg_stat_statements stat column: %s, skip", string(colname.Name))
				continue
			}
		}
	}

	return stats
}

// postgresStatementsStat represents stats values for single statement
type postgresStatementsStat struct {
	datname           string
	usename           string
	queryid           string
	query             string
	calls             float64
	rows              float64
	totalTime         float64
	blkReadTime       float64
	blkWriteTime      float64
	sharedBlksHit     float64
	sharedBlksRead    float64
	sharedBlksDirtied float64
	sharedBlksWritten float64
	localBlksHit      float64
	localBlksRead     float64
	localBlksDirtied  float64
	localBlksWritten  float64
	tempBlksRead      float64
	tempBlksWritten   float64
}

// lNewDBWithPgStatStatements returns connection to the database where pg_stat_statements available for qetting stats.
// Executing this function supposes pg_stat_statements is already available in shared_preload_libraries (checked when
// setting up service).
func NewDBWithPgStatStatements(config *Config) (*store.DB, error) {
	pgconfig, err := pgx.ParseConfig(config.ConnString)
	if err != nil {
		return nil, err
	}

	// Override database name in connection config and use previously found pg_stat_statements source.
	if config.PgStatStatementsSource != "" {
		pgconfig.Database = config.PgStatStatementsSource
	}

	// Establish connection using config.
	conn, err := store.NewDBConfig(pgconfig)
	if err != nil {
		return nil, err
	}

	// Check for pg_stat_statements.
	if conn.IsExtensionAvailable("pg_stat_statements") {
		// Set up pg_stat_statements source. It's unnecessary here, because it's already set on previous execution of that
		// function in pessimistic case, but do it explicitly.
		config.PgStatStatementsSource = conn.Config.Database
		return conn, nil
	}

	// Pessimistic case.
	// If we're here it means pg_stat_statements is not available and we have to walk through all database and looking for it.

	// Drop pg_stat_statements source.
	config.PgStatStatementsSource = ""

	// Get databases list from current connection.
	databases, err := conn.GetDatabases()
	if err != nil {
		conn.Close()
		return nil, err
	}

	// Close connection to current database, it's not interesting anymore.
	conn.Close()

	// Establish connection to each database in the list and check where pg_stat_statements is installed.
	for _, d := range databases {
		pgconfig.Database = d
		conn, err := store.NewDBConfig(pgconfig)
		if err != nil {
			log.Warnf("failed connect to database: %s; skip", err)
			continue
		}

		// If pg_stat_statements found, update source and return connection.
		if conn.IsExtensionAvailable("pg_stat_statements") {
			config.PgStatStatementsSource = conn.Config.Database
			return conn, nil
		}

		// Otherwise close connection and go to next database in the list.
		conn.Close()
	}

	// No luck, if we are here it means all database checked and pg_stat_statements is not found (not installed?)
	return nil, fmt.Errorf("pg_stat_statements not found")
}
