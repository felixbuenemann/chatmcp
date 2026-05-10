package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"syscall"
	"time"

	"modernc.org/sqlite"
)

type Agent struct {
	ID        int64
	Name      string
	Kind      string
	PID       int
	StartedAt int64
}

type ClaimResult struct {
	OK        bool
	Agent     *Agent
	Reason    string // "taken" | "invalid" | ""
	Message   string // detail for "invalid"
	Recovered bool   // true when claim adopted a dead holder's row
}

var nameRegexp = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,32}$`)

func ValidateName(name string) (string, bool) {
	if !nameRegexp.MatchString(name) {
		return "name must match [a-zA-Z0-9._-]{1,32}", false
	}
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "chatmcp") || strings.HasPrefix(lower, "system") {
		return "names starting with 'chatmcp' or 'system' are reserved", false
	}
	return "", true
}

// ClaimName implements the cold-claim path: no existing identity for the caller.
// If the name is free → insert a new agents row.
// If held by the same PID → idempotent success.
// If held by a different live PID → {ok: false, reason: "taken"}.
// If held by a dead PID → adopt the row (identity inherits prior history).
func (db *DB) ClaimName(ctx context.Context, name, kind string, ourPID int) (*ClaimResult, error) {
	if msg, ok := ValidateName(name); !ok {
		return &ClaimResult{OK: false, Reason: "invalid", Message: msg}, nil
	}

	var result *ClaimResult
	err := db.WithWriteLock(func() error {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		var (
			id        int64
			kindCur   string
			pidCur    int
			startedAt int64
		)
		err = tx.QueryRowContext(ctx,
			"SELECT id, kind, pid, started_at FROM agents WHERE name = ?",
			name,
		).Scan(&id, &kindCur, &pidCur, &startedAt)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			now := time.Now().UnixMicro()
			res, err := tx.ExecContext(ctx,
				"INSERT INTO agents (name, kind, pid, started_at) VALUES (?, ?, ?, ?)",
				name, kind, ourPID, now,
			)
			if err != nil {
				if isUniqueViolation(err) {
					result = &ClaimResult{OK: false, Reason: "taken"}
					return tx.Commit()
				}
				return err
			}
			newID, err := res.LastInsertId()
			if err != nil {
				return err
			}
			result = &ClaimResult{OK: true, Agent: &Agent{
				ID: newID, Name: name, Kind: kind, PID: ourPID, StartedAt: now,
			}}

		case err != nil:
			return err

		case pidCur == ourPID:
			result = &ClaimResult{OK: true, Agent: &Agent{
				ID: id, Name: name, Kind: kindCur, PID: pidCur, StartedAt: startedAt,
			}}

		case IsPIDAlive(pidCur):
			result = &ClaimResult{OK: false, Reason: "taken"}

		default:
			now := time.Now().UnixMicro()
			if _, err := tx.ExecContext(ctx,
				"UPDATE agents SET pid = ?, started_at = ?, kind = ? WHERE id = ?",
				ourPID, now, kind, id,
			); err != nil {
				return err
			}
			result = &ClaimResult{OK: true, Recovered: true, Agent: &Agent{
				ID: id, Name: name, Kind: kind, PID: ourPID, StartedAt: now,
			}}
		}

		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// RenameAgent changes the name of an already-bound identity. If the target
// name is held by ANY other agent row (live or dead), the rename is refused
// with reason "taken" — we don't merge identities. The model can pick another.
func (db *DB) RenameAgent(ctx context.Context, agentID int64, newName string, ourPID int) (*ClaimResult, error) {
	if msg, ok := ValidateName(newName); !ok {
		return &ClaimResult{OK: false, Reason: "invalid", Message: msg}, nil
	}

	var result *ClaimResult
	err := db.WithWriteLock(func() error {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		// Is the target free? (Or already us, idempotent.)
		var holderID int64
		err = tx.QueryRowContext(ctx,
			"SELECT id FROM agents WHERE name = ?",
			newName,
		).Scan(&holderID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// free, proceed
		case err != nil:
			return err
		case holderID == agentID:
			// no-op rename to self
			a, err := getAgentTx(ctx, tx, agentID)
			if err != nil {
				return err
			}
			result = &ClaimResult{OK: true, Agent: a}
			return tx.Commit()
		default:
			result = &ClaimResult{OK: false, Reason: "taken"}
			return tx.Commit()
		}

		now := time.Now().UnixMicro()
		if _, err := tx.ExecContext(ctx,
			"UPDATE agents SET name = ?, pid = ?, started_at = ? WHERE id = ?",
			newName, ourPID, now, agentID,
		); err != nil {
			if isUniqueViolation(err) {
				result = &ClaimResult{OK: false, Reason: "taken"}
				return tx.Commit()
			}
			return err
		}
		a, err := getAgentTx(ctx, tx, agentID)
		if err != nil {
			return err
		}
		result = &ClaimResult{OK: true, Agent: a}
		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (db *DB) GetAgent(ctx context.Context, agentID int64) (*Agent, error) {
	var a Agent
	err := db.sql.QueryRowContext(ctx,
		"SELECT id, name, kind, pid, started_at FROM agents WHERE id = ?",
		agentID,
	).Scan(&a.ID, &a.Name, &a.Kind, &a.PID, &a.StartedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (db *DB) GetAgentByName(ctx context.Context, name string) (*Agent, error) {
	var a Agent
	err := db.sql.QueryRowContext(ctx,
		"SELECT id, name, kind, pid, started_at FROM agents WHERE name = ?",
		name,
	).Scan(&a.ID, &a.Name, &a.Kind, &a.PID, &a.StartedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func getAgentTx(ctx context.Context, tx *sql.Tx, id int64) (*Agent, error) {
	var a Agent
	err := tx.QueryRowContext(ctx,
		"SELECT id, name, kind, pid, started_at FROM agents WHERE id = ?",
		id,
	).Scan(&a.ID, &a.Name, &a.Kind, &a.PID, &a.StartedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// IsPIDAlive returns true if a process with the given PID exists.
// On POSIX, signal 0 to a non-existent process returns ESRCH; permission errors
// (EPERM) still mean the process exists.
func IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	// EPERM and other errors: process exists, we just can't signal it.
	return true
}

func isUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		// SQLITE_CONSTRAINT_UNIQUE = 2067
		return sqliteErr.Code() == 2067 || sqliteErr.Code() == 19 // 19 is SQLITE_CONSTRAINT (generic)
	}
	return false
}

// ensure fmt is referenced in case of future use
var _ = fmt.Errorf
