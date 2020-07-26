package collector

import (
	"database/sql"
	"github.com/barcodepro/pgscv/internal/model"
	"github.com/barcodepro/pgscv/internal/store"
	"github.com/jackc/pgproto3/v2"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestPostgresActivityCollector_Update(t *testing.T) {
	var input = pipelineInput{
		required: []string{
			"postgres_activity_conn_total",
			"postgres_activity_max_seconds",
			"postgres_activity_prepared_xact_total",
		},
		collector: NewPostgresActivityCollector,
		service:   model.ServiceTypePostgresql,
	}

	pipeline(t, input)
}

func Test_parsePostgresActivityStats(t *testing.T) {
	var testCases = []struct {
		name string
		res  *store.QueryResult
		want postgresActivityStat
	}{
		{
			name: "normal output",
			res: &store.QueryResult{
				Nrows: 10,
				Ncols: 6,
				Colnames: []pgproto3.FieldDescription{
					{Name: []byte("state")},
					{Name: []byte("wait_event_type")},
					{Name: []byte("wait_event")},
					{Name: []byte("since_start_seconds")},
					{Name: []byte("since_change_seconds")},
					{Name: []byte("query")},
				},
				Rows: [][]sql.NullString{
					{{String: "active", Valid: true}, {}, {}, {String: "10", Valid: true}, {String: "10", Valid: true}, {String: "SELECT active", Valid: true}},
					{{String: "idle", Valid: true}, {}, {}, {String: "100", Valid: true}, {String: "100", Valid: true}, {String: "SELECT idle", Valid: true}},
					{{String: "fastpath function call", Valid: true}, {}, {}, {String: "1", Valid: true}, {String: "1", Valid: true}, {String: "SELECT fastpath", Valid: true}},
					{
						{String: "idle in transaction", Valid: true}, {String: "Client", Valid: true}, {String: "ClentRead", Valid: true},
						{String: "20", Valid: true}, {String: "20", Valid: true}, {String: "SELECT idle in transaction", Valid: true},
					},
					{
						{String: "idle in transaction (aborted)", Valid: true}, {String: "Client", Valid: true}, {String: "ClentRead", Valid: true},
						{String: "15", Valid: true}, {String: "15", Valid: true}, {String: "SELECT idle in transaction", Valid: true},
					},
					{{String: "active", Valid: true}, {}, {}, {String: "5", Valid: true}, {String: "5", Valid: true}, {String: "VACUUM example1", Valid: true}},
					{{String: "active", Valid: true}, {}, {}, {String: "7", Valid: true}, {String: "6", Valid: true}, {String: "analyze example2", Valid: true}},
					{{String: "active", Valid: true}, {}, {}, {String: "9", Valid: true}, {String: "7", Valid: true}, {String: "autovacuum: VACUUM example3", Valid: true}},
					{
						{String: "active", Valid: true}, {String: "Lock", Valid: true}, {String: "transactionid", Valid: true},
						{String: "20", Valid: true}, {String: "13", Valid: true}, {String: "UPDATE waiting", Valid: true},
					},
					{
						{String: "active", Valid: true}, {String: "Lock", Valid: true}, {String: "transactionid", Valid: true},
						{String: "12", Valid: true}, {String: "12", Valid: true}, {String: "VACUUM example2", Valid: true},
					},
				},
			},
			want: postgresActivityStat{total: 10, active: 6, idle: 1, idlexact: 2, other: 1, waiting: 2, maxRunUser: 20, maxRunMaint: 9, maxWaitUser: 13, maxWaitMaint: 12},
		},
	}

	for _, tc := range testCases {
		got := parsePostgresActivityStats(tc.res)
		assert.EqualValues(t, tc.want, got)
	}
}
