package providers

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
)

// kiroEvent holds a single decoded AWS Event Stream frame.
type kiroEvent struct {
	EventType   string // from :event-type header
	MessageType string // from :message-type header
	Payload     []byte // raw JSON payload
}

// parseKiroFrame reads and validates a single AWS Event Stream frame from r.
// Frame layout:
//
//	[4] total_length  [4] headers_length  [4] prelude_crc
//	[headers_length] headers
//	[total_length - headers_length - 16] payload
//	[4] message_crc
func parseKiroFrame(r io.Reader) (kiroEvent, error) {
	// Read the 12-byte prelude
	var prelude [12]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return kiroEvent{}, err
	}
	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	preludeCRC := binary.BigEndian.Uint32(prelude[8:12])

	// Validate prelude CRC (first 8 bytes)
	computed := crc32.ChecksumIEEE(prelude[0:8])
	if computed != preludeCRC {
		return kiroEvent{}, fmt.Errorf("kiro eventstream: prelude CRC mismatch (got %08x, want %08x)", computed, preludeCRC)
	}

	// Sanity-check sizes
	const minFrameSize = 16 // prelude(12) + message_crc(4)
	if totalLen < minFrameSize || headersLen > totalLen-minFrameSize {
		return kiroEvent{}, fmt.Errorf("kiro eventstream: invalid frame lengths total=%d headers=%d", totalLen, headersLen)
	}

	// Read remaining bytes: headers + payload + message_crc
	remaining := int(totalLen) - 12
	rest := make([]byte, remaining)
	if _, err := io.ReadFull(r, rest); err != nil {
		return kiroEvent{}, fmt.Errorf("kiro eventstream: reading frame body: %w", err)
	}

	// Validate message CRC over entire message minus final 4 bytes
	msgCRC := binary.BigEndian.Uint32(rest[remaining-4:])
	msgComputed := crc32.ChecksumIEEE(append(prelude[:], rest[:remaining-4]...))
	if msgComputed != msgCRC {
		return kiroEvent{}, fmt.Errorf("kiro eventstream: message CRC mismatch (got %08x, want %08x)", msgComputed, msgCRC)
	}

	headerBytes := rest[:headersLen]
	payloadBytes := rest[headersLen : remaining-4]

	// Parse headers
	event := kiroEvent{Payload: payloadBytes}
	if err := parseKiroHeaders(headerBytes, &event); err != nil {
		return kiroEvent{}, err
	}
	return event, nil
}

// parseKiroHeaders decodes the concatenated header block.
// Each header: [1 name_len][name][1 type][2 value_len][value]
func parseKiroHeaders(data []byte, event *kiroEvent) error {
	i := 0
	for i < len(data) {
		if i >= len(data) {
			break
		}
		nameLen := int(data[i])
		i++
		if i+nameLen > len(data) {
			return fmt.Errorf("kiro eventstream: header name truncated")
		}
		name := string(data[i : i+nameLen])
		i += nameLen

		if i >= len(data) {
			return fmt.Errorf("kiro eventstream: header type missing")
		}
		// type byte; 7 = string
		i++ // skip type byte

		if i+2 > len(data) {
			return fmt.Errorf("kiro eventstream: header value_len truncated")
		}
		valLen := int(binary.BigEndian.Uint16(data[i : i+2]))
		i += 2
		if i+valLen > len(data) {
			return fmt.Errorf("kiro eventstream: header value truncated")
		}
		value := string(data[i : i+valLen])
		i += valLen

		switch name {
		case ":event-type":
			event.EventType = value
		case ":message-type":
			event.MessageType = value
		}
	}
	return nil
}

// kiroAssistantResponsePayload is the JSON body of assistantResponseEvent frames.
type kiroAssistantResponsePayload struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
}

// kiroToolUsePayload is the JSON body of toolUseEvent frames.
type kiroToolUsePayload struct {
	ToolUse struct {
		ToolUseID string `json:"toolUseId"`
		Name      string `json:"name"`
		Input     string `json:"input"` // JSON string that we re-encode as RawMessage
	} `json:"toolUse"`
}

// kiroErrorPayload is the JSON body of errorEvent frames.
type kiroErrorPayload struct {
	Error struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// readKiroEvents reads AWS Event Stream frames from reader, converts them to
// providers.Event values, and sends them to out. It returns when the stream
// ends (EOF) or a terminal error/event is encountered.
func readKiroEvents(ctx context.Context, reader io.Reader, out chan<- Event) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		frame, err := parseKiroFrame(reader)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}

		// Only process data frames
		if frame.MessageType != "event" {
			continue
		}

		switch frame.EventType {
		case "assistantResponseEvent":
			var p kiroAssistantResponsePayload
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			if p.Content.Text == "" {
				continue
			}
			if !emitProviderEvent(ctx, out, Event{Type: "text", Text: p.Content.Text}) {
				return ctx.Err()
			}

		case "toolUseEvent":
			var p kiroToolUsePayload
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			inputRaw := json.RawMessage(p.ToolUse.Input)
			if !json.Valid(inputRaw) {
				inputRaw = json.RawMessage("{}")
			}
			tc := &ToolCall{
				ID:    strings.TrimSpace(p.ToolUse.ToolUseID),
				Name:  strings.TrimSpace(p.ToolUse.Name),
				Input: inputRaw,
			}
			if !emitProviderEvent(ctx, out, Event{Type: "tool_call", ToolCall: tc}) {
				return ctx.Err()
			}

		case "endEvent", "completeEvent":
			_ = emitProviderEvent(ctx, out, Event{Type: "done", Done: true})
			return nil

		case "errorEvent":
			var p kiroErrorPayload
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: "kiro: stream error"})
				return nil
			}
			msg := strings.TrimSpace(p.Error.Message)
			if msg == "" {
				msg = "kiro: upstream error"
			}
			_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: msg})
			return nil
		}
	}
}
