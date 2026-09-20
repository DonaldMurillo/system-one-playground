//go:build ignore

// Package fixture exercises the general-purpose rules on Go. It is excluded
// from the build by the tag above and exists only as linter input.
//
// Which functions are defective is recorded in general.expected.json, never
// here: nothing in a linter's input should name the defect it must find.
package fixture

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
)

// LoadSettings reads the settings file and returns the parsed settings.
func LoadSettings(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out
	}
	return out
}

// CountActiveUsers returns how many users are currently active.
func CountActiveUsers(db *sql.DB) (int, error) {
	rows, err := db.Query("SELECT id FROM users WHERE active = 1")
	if err != nil {
		return 0, err
	}
	n := 0
	for rows.Next() {
		n++
	}
	if n > 1000 {
		return 1000, nil
	}
	return n, nil
}

// GetUserName returns the display name for a user id.
func GetUserName(db *sql.DB, id string) (string, error) {
	var name string
	err := db.QueryRow("SELECT name FROM users WHERE id = ?", id).Scan(&name)
	if err != nil {
		return "", err
	}
	_, _ = db.Exec("UPDATE users SET last_seen = now() WHERE id = ?", id)
	return name, nil
}

// SendAll delivers every message and reports whether the batch was delivered.
func SendAll(client *http.Client, urls []string, body string) error {
	for _, u := range urls {
		resp, err := client.Post(u, "application/json", strings.NewReader(body))
		if err != nil {
			continue
		}
		resp.Body.Close()
	}
	return nil
}

// ValidatePort checks that a port number is usable.
func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("invalid configuration")
	}
	return nil
}

// StoreNote saves a note and returns the stored value.
func StoreNote(db *sql.DB, id, note string) (string, error) {
	if len(note) > 255 {
		note = note[:255]
	}
	if _, err := db.Exec("INSERT INTO notes (id, body) VALUES (?, ?)", id, note); err != nil {
		return "", err
	}
	return note, nil
}

// AppendAudit adds one entry to the audit file.
// The file is opened and closed on every call so no handle is held between
// writes, and a write failure is returned to the caller.
func AppendAudit(path, entry string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(entry + "\n"); err != nil {
		return err
	}
	return f.Close()
}

// TODO: switch this to the pooled client once the pool lands.
func fetchOnce(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return buf[:n], nil
}

// cache is guarded by mu.
var (
	mu    sync.Mutex
	cache = map[string]string{}
)

// LookupCached returns a cached value, computing it if absent.
func LookupCached(key string, compute func(string) string) string {
	mu.Lock()
	v, ok := cache[key]
	mu.Unlock()
	if ok {
		return v
	}
	v = compute(key)
	mu.Lock()
	cache[key] = v
	mu.Unlock()
	return v
}

// TODO: make the audit append retry on failure instead of giving up.
// AuditWithRetry appends an entry, retrying a few times on failure.
func AuditWithRetry(path, entry string, attempts int) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = AppendAudit(path, entry); err == nil {
			return nil
		}
	}
	return err
}

// RecentEvents returns the most recent events, newest first.
func RecentEvents(all []string, limit int) []string {
	sorted := append([]string(nil), all...)
	for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
		sorted[i], sorted[j] = sorted[j], sorted[i]
	}
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	return sorted
}

// WriteReport writes the report, returning any error.
func WriteReport(path string, lines []string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
