package controller

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver

	"github.com/skinnyrad/agent-cluster/internal/schemas"
)

// store persists jobs and their events in SQLite.
type store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id          TEXT PRIMARY KEY,
	session_id  TEXT NOT NULL,
	prompt      TEXT NOT NULL,
	status      TEXT NOT NULL,
	subtasks    TEXT NOT NULL,
	results     TEXT NOT NULL,
	synthesis   TEXT NOT NULL,
	error       TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	finished_at TEXT
);
CREATE INDEX IF NOT EXISTS jobs_session ON jobs (session_id, created_at);
CREATE INDEX IF NOT EXISTS jobs_created ON jobs (created_at);
CREATE TABLE IF NOT EXISTS events (
	job_id TEXT NOT NULL,
	seq    INTEGER NOT NULL,
	data   TEXT NOT NULL,
	PRIMARY KEY (job_id, seq)
);
`

func openStore(path string) (*store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection serializes writes, which SQLite needs anyway, and keeps an
	// in-memory database alive.
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{"PRAGMA busy_timeout = 5000", "PRAGMA journal_mode = WAL", schema} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &store{db: db}, nil
}

func (s *store) Close() error { return s.db.Close() }

func (s *store) saveJob(j *schemas.Job) error {
	subtasks, _ := json.Marshal(j.Subtasks)
	results, _ := json.Marshal(j.Results)
	var finished any
	if j.FinishedAt != nil {
		finished = j.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT INTO jobs (id, session_id, prompt, status, subtasks, results, synthesis, error, created_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			status = excluded.status, subtasks = excluded.subtasks, results = excluded.results,
			synthesis = excluded.synthesis, error = excluded.error, finished_at = excluded.finished_at`,
		j.ID, j.SessionID, j.Prompt, j.Status, string(subtasks), string(results), j.Synthesis, j.Error,
		j.CreatedAt.UTC().Format(time.RFC3339Nano), finished)
	return err
}

func (s *store) addEvent(e *schemas.Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO events (job_id, seq, data) VALUES (?, ?, ?)`, e.JobID, e.Seq, string(data))
	return err
}

const jobColumns = `id, session_id, prompt, status, subtasks, results, synthesis, error, created_at, finished_at`

func scanJob(row interface{ Scan(...any) error }) (*schemas.Job, error) {
	var j schemas.Job
	var subtasks, results, created string
	var finished sql.NullString
	if err := row.Scan(&j.ID, &j.SessionID, &j.Prompt, &j.Status, &subtasks, &results,
		&j.Synthesis, &j.Error, &created, &finished); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(subtasks), &j.Subtasks); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(results), &j.Results); err != nil {
		return nil, err
	}
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if finished.Valid {
		t, _ := time.Parse(time.RFC3339Nano, finished.String)
		j.FinishedAt = &t
	}
	return &j, nil
}

// job returns the stored job, or nil if there is none.
func (s *store) job(id string) (*schemas.Job, error) {
	j, err := scanJob(s.db.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

// jobs returns the most recent jobs, newest first, optionally for one session.
func (s *store) jobs(sessionID string, limit int) ([]schemas.Job, error) {
	query := `SELECT ` + jobColumns + ` FROM jobs`
	args := []any{}
	if sessionID != "" {
		query += ` WHERE session_id = ?`
		args = append(args, sessionID)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []schemas.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func (s *store) events(jobID string) ([]schemas.Event, error) {
	rows, err := s.db.Query(`SELECT data FROM events WHERE job_id = ? ORDER BY seq`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []schemas.Event
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var e schemas.Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// turn is one finished exchange in a session.
type turn struct {
	Prompt    string
	Synthesis string
}

// history returns the last n finished exchanges of a session, oldest first.
func (s *store) history(sessionID string, n int) ([]turn, error) {
	if sessionID == "" || n <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT prompt, synthesis FROM jobs
		WHERE session_id = ? AND status = ?
		ORDER BY created_at DESC LIMIT ?`, sessionID, schemas.JobDone, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []turn
	for rows.Next() {
		var t turn
		if err := rows.Scan(&t.Prompt, &t.Synthesis); err != nil {
			return nil, err
		}
		out = append([]turn{t}, out...)
	}
	return out, rows.Err()
}
