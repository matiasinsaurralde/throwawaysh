package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SessionSummary struct {
	ID              string    `json:"id"`
	RemoteAddr      string    `json:"remote_addr"`
	RemoteIP        string    `json:"remote_ip"`
	Username        string    `json:"username"`
	StartedAt       time.Time `json:"started_at"`
	DurationSeconds int64     `json:"duration_seconds"`
	CountryCode     string    `json:"country_code,omitempty"`
	CountryFlag     string    `json:"country_flag,omitempty"`
}

type trackedSession struct {
	id         string
	remoteAddr string
	remoteIP   string
	username   string
	startedAt  time.Time
	logPath    string

	mu          sync.Mutex
	logFile     *os.File
	subscribers map[chan string]struct{}
	done        chan struct{}
	ended       bool
}

type SessionTracker struct {
	logger *slog.Logger

	mu       sync.RWMutex
	sessions map[string]*trackedSession
}

func NewSessionTracker(logger *slog.Logger) *SessionTracker {
	return &SessionTracker{
		logger:   logger,
		sessions: make(map[string]*trackedSession),
	}
}

func (t *SessionTracker) Start(remoteAddr, username string) (*trackedSession, error) {
	sessionID, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("create session id: %w", err)
	}

	logPath := filepath.Join(os.TempDir(), "throwawaysh-session-"+sessionID+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session log file: %w", err)
	}

	session := &trackedSession{
		id:          sessionID,
		remoteAddr:  remoteAddr,
		remoteIP:    parseRemoteIP(remoteAddr),
		username:    username,
		startedAt:   time.Now().UTC(),
		logPath:     logPath,
		logFile:     logFile,
		subscribers: make(map[chan string]struct{}),
		done:        make(chan struct{}),
	}

	t.mu.Lock()
	t.sessions[sessionID] = session
	t.mu.Unlock()

	t.logger.Info(
		"session tracking started",
		"event", "session_tracker_started",
		"session_id", session.id,
		"remote_ip", session.remoteIP,
		"username", session.username,
		"log_path", session.logPath,
	)
	return session, nil
}

func (t *SessionTracker) End(sessionID string) {
	t.mu.Lock()
	session, ok := t.sessions[sessionID]
	if ok {
		delete(t.sessions, sessionID)
	}
	t.mu.Unlock()
	if !ok {
		return
	}

	session.mu.Lock()
	if session.ended {
		session.mu.Unlock()
		return
	}
	session.ended = true
	_ = session.logFile.Close()
	for sub := range session.subscribers {
		delete(session.subscribers, sub)
		close(sub)
	}
	close(session.done)
	session.mu.Unlock()

	_ = os.Remove(session.logPath)
	t.logger.Info(
		"session tracking ended",
		"event", "session_tracker_ended",
		"session_id", session.id,
		"log_path", session.logPath,
	)
}

func (t *SessionTracker) List() []SessionSummary {
	t.mu.RLock()
	snapshots := make([]SessionSummary, 0, len(t.sessions))
	for _, session := range t.sessions {
		duration := int64(time.Since(session.startedAt).Seconds())
		if duration < 0 {
			duration = 0
		}
		snapshots = append(snapshots, SessionSummary{
			ID:              session.id,
			RemoteAddr:      session.remoteAddr,
			RemoteIP:        session.remoteIP,
			Username:        session.username,
			StartedAt:       session.startedAt,
			DurationSeconds: duration,
			CountryCode:     guessCountryCode(session.remoteIP),
			CountryFlag:     guessCountryFlag(session.remoteIP),
		})
	}
	t.mu.RUnlock()
	return snapshots
}

func (t *SessionTracker) OpenStream(sessionID string) (string, <-chan string, <-chan struct{}, func(), error) {
	t.mu.RLock()
	session, ok := t.sessions[sessionID]
	t.mu.RUnlock()
	if !ok {
		return "", nil, nil, nil, os.ErrNotExist
	}

	initialBytes, err := os.ReadFile(session.logPath)
	if err != nil {
		return "", nil, nil, nil, err
	}

	updateCh := make(chan string, 128)
	session.mu.Lock()
	if session.ended {
		session.mu.Unlock()
		close(updateCh)
		return string(initialBytes), updateCh, session.done, func() {}, nil
	}
	session.subscribers[updateCh] = struct{}{}
	session.mu.Unlock()

	cancel := func() {
		session.mu.Lock()
		if _, exists := session.subscribers[updateCh]; exists {
			delete(session.subscribers, updateCh)
			if !session.ended {
				close(updateCh)
			}
		}
		session.mu.Unlock()
	}

	return string(initialBytes), updateCh, session.done, cancel, nil
}

func (s *trackedSession) ID() string {
	return s.id
}

func (s *trackedSession) LogInput(payload []byte) {
	s.appendLogChunk("IN ", payload)
}

func (s *trackedSession) LogOutput(payload []byte) {
	s.appendLogChunk("OUT", payload)
}

func (s *trackedSession) appendLogChunk(direction string, payload []byte) {
	chunk := strings.TrimSpace(strconv.QuoteToASCII(string(payload)))
	if chunk == `""` || chunk == "" {
		return
	}
	chunk = strings.Trim(chunk, `"`)
	entry := fmt.Sprintf("[%s] %s %s\n", time.Now().UTC().Format(time.RFC3339), direction, chunk)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	if _, err := s.logFile.WriteString(entry); err != nil {
		return
	}
	for sub := range s.subscribers {
		select {
		case sub <- entry:
		default:
		}
	}
}

func parseRemoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func newSessionID() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	hexValue := hex.EncodeToString(randomBytes)
	return fmt.Sprintf(
		"%s-%s-%s-%s-%s",
		hexValue[0:8],
		hexValue[8:12],
		hexValue[12:16],
		hexValue[16:20],
		hexValue[20:32],
	), nil
}

func guessCountryCode(remoteIP string) string {
	if remoteIP == "" {
		return ""
	}
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return ""
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return "LO"
	}
	return ""
}

func guessCountryFlag(remoteIP string) string {
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return ""
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return "🏠"
	}
	return "🌐"
}
