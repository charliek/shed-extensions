package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry is a single audit log record.
type AuditEntry struct {
	Timestamp string `json:"ts"`
	Shed      string `json:"shed"`
	Namespace string `json:"ns"`
	Operation string `json:"op"`
	Result    string `json:"result"`
	Detail    string `json:"detail,omitempty"`
	Approval  string `json:"approval"`
}

// AuditLogger writes JSON lines to the audit log file.
type AuditLogger struct {
	mu      sync.Mutex
	file    *os.File
	encoder *json.Encoder
	logger  *slog.Logger
}

// NewAuditLogger creates an audit logger. If the config disables logging or the
// file cannot be opened, returns a no-op logger that silently discards entries.
func NewAuditLogger(cfg LogConfig, logger *slog.Logger) *AuditLogger {
	if !cfg.Enabled {
		return &AuditLogger{logger: logger}
	}

	// Ensure directory exists
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		logger.Warn("failed to create audit log directory", "path", dir, "error", err)
		return &AuditLogger{logger: logger}
	}

	f, err := os.OpenFile(cfg.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		logger.Warn("failed to open audit log", "path", cfg.Path, "error", err)
		return &AuditLogger{logger: logger}
	}

	logger.Info("audit logging enabled", "path", cfg.Path)
	return &AuditLogger{
		file:    f,
		encoder: json.NewEncoder(f),
		logger:  logger,
	}
}

// Log writes an audit entry. Safe for concurrent use.
func (a *AuditLogger) Log(shed, namespace, operation, result, detail, approval string) {
	if a.file == nil {
		return
	}

	entry := AuditEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Shed:      shed,
		Namespace: namespace,
		Operation: operation,
		Result:    result,
		Detail:    detail,
		Approval:  approval,
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.encoder.Encode(entry); err != nil {
		a.logger.Error("failed to write audit log", "error", err)
	}
}

// Close closes the audit log file.
func (a *AuditLogger) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file != nil {
		a.file.Close()
		a.file = nil
	}
}
