package agentproto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	FrameStart  = byte(1)
	FrameStdin  = byte(2)
	FrameResize = byte(3)
	FrameSignal = byte(4)
	FrameOutput = byte(5)
	FrameExit   = byte(6)
	FrameError  = byte(7)
)

type StartPayload struct {
	Term    string `json:"term"`
	Rows    uint32 `json:"rows"`
	Columns uint32 `json:"columns"`
	Command string `json:"command,omitempty"`
}

type ResizePayload struct {
	Rows    uint32 `json:"rows"`
	Columns uint32 `json:"columns"`
}

type ExitPayload struct {
	Code int `json:"code"`
}

func ReadFrame(reader io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}

	frameType := header[0]
	size := binary.BigEndian.Uint32(header[1:])
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return frameType, payload, nil
}

func WriteFrame(writer io.Writer, frameType byte, payload []byte) error {
	header := make([]byte, 5)
	header[0] = frameType
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := writer.Write(header); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := writer.Write(payload)
	return err
}

func EncodeJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func DecodeJSON(payload []byte, out any) error {
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode json payload: %w", err)
	}
	return nil
}
