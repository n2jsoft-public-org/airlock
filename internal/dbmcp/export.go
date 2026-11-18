package dbmcp

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
)

// exportTimeout bounds an export, and the server's statement timeout during it (exportSetup): a
// large result needs more than queryTimeout to stream through a tunnel, but a forgotten statement
// must not hold one of the pool's two connections for long.
const exportTimeout = 5 * time.Minute

// maxExportBytes caps the file of an export, to protect the disk; a variable so tests can lower it.
var maxExportBytes int64 = 1 << 30

// Export formats.
const (
	formatCSV   = "csv"
	formatJSONL = "jsonl"
)

// ExportResult describes the file an export wrote. It never holds the rows: they stay local.
type ExportResult struct {
	Path      string   `json:"path"`
	Format    string   `json:"format"`
	Columns   []string `json:"columns"`
	RowCount  int64    `json:"row_count"`
	Bytes     int64    `json:"bytes"`
	Truncated bool     `json:"truncated"`
}

func (q *sqlQuerier) Export(ctx context.Context, database, query, format string) (ExportResult, error) {
	if err := CheckReadOnly(q.dialect.engine, query); err != nil {
		return ExportResult{}, err
	}
	if format == "" {
		format = formatCSV
	}
	if format != formatCSV && format != formatJSONL {
		return ExportResult{}, fmt.Errorf("unknown format %q: use csv or jsonl", format)
	}
	if q.exportDir == "" {
		return ExportResult{}, errors.New("exports are disabled: airlock found no directory to write them to")
	}
	var result ExportResult
	err := q.withPool(ctx, database, func(db *sql.DB) (err error) {
		result, err = q.exportFrom(ctx, db, database, query, format)
		return err
	})
	return result, err
}

// exportFrom writes the rows of query to a new file of the export directory; the file is removed
// when the export fails.
func (q *sqlQuerier) exportFrom(ctx context.Context, db *sql.DB, database, query, format string) (result ExportResult, err error) {
	if mkdirErr := os.MkdirAll(q.exportDir, 0o700); mkdirErr != nil {
		return ExportResult{}, mkdirErr
	}
	file, err := os.CreateTemp(q.exportDir, q.exportPattern(database, format))
	if err != nil {
		return ExportResult{}, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(file.Name()) //nolint:errcheck // the export already failed; a leftover file is all it costs.
			result = ExportResult{}
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, exportTimeout)
	defer cancel()
	buffered := bufio.NewWriter(file)
	err = q.readTx(ctx, db, q.dialect.exportSetup, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, query)
		if queryErr != nil {
			return queryErr
		}
		var writeErr error
		result, writeErr = writeRows(rows, &countingWriter{w: buffered}, format)
		stopped := writeErr != nil || result.Truncated
		if stopped {
			// Cancel before Close, as readFrom does: pgx would otherwise stream the rest of the result.
			cancel()
		}
		_ = rows.Close() //nolint:errcheck // a stopped export fails Close with the cancellation; writeErr has the real error.
		if !stopped {
			for _, statement := range q.dialect.exportReset {
				_, _ = tx.ExecContext(ctx, statement) //nolint:errcheck // best effort: the pool's next reads keep a longer server timeout at worst.
			}
		}
		return writeErr
	})
	if err == nil {
		err = buffered.Flush()
	}
	result.Path = file.Name()
	return result, err
}

// exportPattern names the file of an export: time and database first, then os.CreateTemp's random
// part, so two exports never collide.
func (q *sqlQuerier) exportPattern(database, format string) string {
	if database == "" {
		q.mu.Lock()
		database = q.defaultDB
		q.mu.Unlock()
	}
	safe := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, database)
	return time.Now().Format("20060102-150405") + "-" + safe + "-*." + format
}

// writeRows writes every row of rows to out in format, stopping after maxExportBytes (counted
// past CSV's own buffer, so a CSV export may go over by a buffer).
func writeRows(rows *sql.Rows, out *countingWriter, format string) (ExportResult, error) {
	columns, err := rows.Columns()
	if err != nil {
		return ExportResult{}, err
	}
	types := columnTypes(rows, len(columns))
	write, flush, err := newRowWriter(out, format, columns)
	if err != nil {
		return ExportResult{}, err
	}
	result := ExportResult{Format: format, Columns: columns}
	for rows.Next() {
		if out.n >= maxExportBytes {
			result.Truncated = true
			break
		}
		values, err := scanValues(rows, types)
		if err != nil {
			return ExportResult{}, err
		}
		if err := write(values); err != nil {
			return ExportResult{}, err
		}
		result.RowCount++
	}
	if err := rows.Err(); err != nil {
		return ExportResult{}, err
	}
	if err := flush(); err != nil {
		return ExportResult{}, err
	}
	result.Bytes = out.n
	return result, nil
}

// newRowWriter returns the functions writing a row of columns to w in format, and flushing what
// it buffers itself.
// CSV starts with a header row and writes NULL as an empty field; JSONL writes one object per row,
// its keys in column order.
func newRowWriter(w io.Writer, format string, columns []string) (write func([]any) error, flush func() error, err error) {
	if format == formatJSONL {
		keys := make([][]byte, len(columns))
		for i, column := range columns {
			keys[i], _ = json.Marshal(column) //nolint:errcheck // a string always marshals.
		}
		write = func(values []any) error {
			line := []byte{'{'}
			for i, value := range values {
				encoded, err := json.Marshal(value)
				if err != nil {
					return err
				}
				if i > 0 {
					line = append(line, ',')
				}
				line = append(append(append(line, keys[i]...), ':'), encoded...)
			}
			_, err := w.Write(append(line, '}', '\n'))
			return err
		}
		return write, func() error { return nil }, nil
	}
	writer := csv.NewWriter(w)
	if err := writer.Write(columns); err != nil {
		return nil, nil, err
	}
	record := make([]string, len(columns))
	write = func(values []any) error {
		for i, value := range values {
			record[i] = text(value)
		}
		return writer.Write(record)
	}
	flush = func() error {
		writer.Flush()
		return writer.Error()
	}
	return write, flush, nil
}

// countingWriter counts the bytes written to w.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
