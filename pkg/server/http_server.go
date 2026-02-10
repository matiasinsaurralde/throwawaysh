package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

func (s *Server) newHTTPServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndexPage)
	mux.HandleFunc("GET /api/sessions", s.handleSessionList)
	mux.HandleFunc("GET /api/sessions/{id}/stream", s.handleSessionStream)

	return &http.Server{
		Addr:    s.cfg.HTTPListenAddr,
		Handler: mux,
	}
}

func (s *Server) runHTTPServer(ctx context.Context) error {
	server := s.newHTTPServer()
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()

	err := <-errCh
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("http server failed: %w", err)
}

func (s *Server) handleIndexPage(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte(indexHTMLPage))
}

func (s *Server) handleSessionList(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(map[string]any{
		"sessions": s.tracker.List(),
	})
}

func (s *Server) handleSessionStream(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sessionID := request.PathValue("id")
	initial, updates, sessionDone, cancel, err := s.tracker.OpenStream(sessionID)
	if err != nil {
		http.Error(writer, "session not found", http.StatusNotFound)
		return
	}
	defer cancel()

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")

	writeSSEChunk(writer, "chunk", initial)
	flusher.Flush()

	for {
		select {
		case <-request.Context().Done():
			return
		case <-sessionDone:
			writeSSEChunk(writer, "end", "session finished")
			flusher.Flush()
			return
		case chunk, ok := <-updates:
			if !ok {
				writeSSEChunk(writer, "end", "session finished")
				flusher.Flush()
				return
			}
			writeSSEChunk(writer, "chunk", chunk)
			flusher.Flush()
		}
	}
}

func writeSSEChunk(writer http.ResponseWriter, eventType, chunk string) {
	payloadBytes, err := json.Marshal(map[string]string{
		"chunk": chunk,
	})
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(writer, "event: %s\n", eventType)
	_, _ = fmt.Fprintf(writer, "data: %s\n\n", payloadBytes)
}
