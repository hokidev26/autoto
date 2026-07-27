package providers

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"testing"
)

// buildKiroFrame constructs a valid AWS Event Stream frame with the given
// headers (pre-encoded) and payload bytes.
func buildKiroFrame(headers, payload []byte) []byte {
	headersLen := uint32(len(headers))
	payloadLen := uint32(len(payload))
	totalLen := 12 + headersLen + payloadLen + 4 // prelude + headers + payload + msg_crc

	buf := make([]byte, totalLen)
	binary.BigEndian.PutUint32(buf[0:], totalLen)
	binary.BigEndian.PutUint32(buf[4:], headersLen)

	preludeCRC := crc32.ChecksumIEEE(buf[0:8])
	binary.BigEndian.PutUint32(buf[8:], preludeCRC)

	copy(buf[12:], headers)
	copy(buf[12+headersLen:], payload)

	msgCRC := crc32.ChecksumIEEE(buf[:totalLen-4])
	binary.BigEndian.PutUint32(buf[totalLen-4:], msgCRC)
	return buf
}

// buildKiroHeader encodes a single event stream header (type=7, string).
func buildKiroHeader(name, value string) []byte {
	var b bytes.Buffer
	b.WriteByte(byte(len(name)))
	b.WriteString(name)
	b.WriteByte(7) // string type
	vlen := make([]byte, 2)
	binary.BigEndian.PutUint16(vlen, uint16(len(value)))
	b.Write(vlen)
	b.WriteString(value)
	return b.Bytes()
}

func buildKiroEventHeaders(eventType, messageType string) []byte {
	var b bytes.Buffer
	b.Write(buildKiroHeader(":event-type", eventType))
	b.Write(buildKiroHeader(":message-type", messageType))
	return b.Bytes()
}

// TestParseKiroFrame_RoundTrip verifies that a hand-crafted frame parses correctly.
func TestParseKiroFrame_RoundTrip(t *testing.T) {
	payload := []byte(`{"content":{"text":"hello"}}`)
	headers := buildKiroEventHeaders("assistantResponseEvent", "event")
	frame := buildKiroFrame(headers, payload)

	ev, err := parseKiroFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("parseKiroFrame error: %v", err)
	}
	if ev.EventType != "assistantResponseEvent" {
		t.Errorf("EventType = %q, want %q", ev.EventType, "assistantResponseEvent")
	}
	if ev.MessageType != "event" {
		t.Errorf("MessageType = %q, want %q", ev.MessageType, "event")
	}
	if string(ev.Payload) != string(payload) {
		t.Errorf("Payload = %q, want %q", ev.Payload, payload)
	}
}

// TestReadKiroEvents_TextEvent verifies an assistantResponseEvent emits a text provider.Event.
func TestReadKiroEvents_TextEvent(t *testing.T) {
	payload := []byte(`{"content":{"text":"world"}}`)
	headers := buildKiroEventHeaders("assistantResponseEvent", "event")
	frame := buildKiroFrame(headers, payload)
	// Follow with endEvent so the reader terminates cleanly.
	endHeaders := buildKiroEventHeaders("endEvent", "event")
	endFrame := buildKiroFrame(endHeaders, []byte(`{}`))

	r := io.MultiReader(bytes.NewReader(frame), bytes.NewReader(endFrame))
	out := make(chan Event, 8)
	ctx := context.Background()

	err := readKiroEvents(ctx, r, out)
	close(out)
	if err != nil {
		t.Fatalf("readKiroEvents: %v", err)
	}

	var events []Event
	for ev := range out {
		events = append(events, ev)
	}
	if len(events) < 1 {
		t.Fatal("expected at least one event")
	}
	if events[0].Type != "text" {
		t.Errorf("event[0].Type = %q, want %q", events[0].Type, "text")
	}
	if events[0].Text != "world" {
		t.Errorf("event[0].Text = %q, want %q", events[0].Text, "world")
	}
}

// TestReadKiroEvents_ToolUseEvent verifies a toolUseEvent emits a tool_call provider.Event.
func TestReadKiroEvents_ToolUseEvent(t *testing.T) {
	inputJSON := `{"path":"/tmp/foo"}`
	body := map[string]any{
		"toolUse": map[string]any{
			"toolUseId": "tu-1",
			"name":      "read_file",
			"input":     inputJSON,
		},
	}
	payload, _ := json.Marshal(body)
	headers := buildKiroEventHeaders("toolUseEvent", "event")
	frame := buildKiroFrame(headers, payload)
	endFrame := buildKiroFrame(buildKiroEventHeaders("endEvent", "event"), []byte(`{}`))

	r := io.MultiReader(bytes.NewReader(frame), bytes.NewReader(endFrame))
	out := make(chan Event, 8)

	err := readKiroEvents(context.Background(), r, out)
	close(out)
	if err != nil {
		t.Fatalf("readKiroEvents: %v", err)
	}

	var events []Event
	for ev := range out {
		events = append(events, ev)
	}
	if len(events) < 1 {
		t.Fatal("expected at least one event")
	}
	got := events[0]
	if got.Type != "tool_call" {
		t.Errorf("Type = %q, want tool_call", got.Type)
	}
	if got.ToolCall == nil {
		t.Fatal("ToolCall is nil")
	}
	if got.ToolCall.Name != "read_file" {
		t.Errorf("ToolCall.Name = %q, want read_file", got.ToolCall.Name)
	}
	if got.ToolCall.ID != "tu-1" {
		t.Errorf("ToolCall.ID = %q, want tu-1", got.ToolCall.ID)
	}
	if !json.Valid(got.ToolCall.Input) {
		t.Errorf("ToolCall.Input is not valid JSON: %s", got.ToolCall.Input)
	}
}

// TestReadKiroEvents_ErrorEvent verifies an errorEvent emits an error provider.Event.
func TestReadKiroEvents_ErrorEvent(t *testing.T) {
	payload := []byte(`{"error":{"message":"quota exceeded","code":"QUOTA_EXCEEDED"}}`)
	headers := buildKiroEventHeaders("errorEvent", "event")
	frame := buildKiroFrame(headers, payload)

	r := bytes.NewReader(frame)
	out := make(chan Event, 8)

	err := readKiroEvents(context.Background(), r, out)
	close(out)
	if err != nil {
		t.Fatalf("readKiroEvents: %v", err)
	}

	var events []Event
	for ev := range out {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("expected an error event")
	}
	if events[0].Type != "error" {
		t.Errorf("Type = %q, want error", events[0].Type)
	}
	if events[0].Text != "quota exceeded" {
		t.Errorf("Text = %q, want quota exceeded", events[0].Text)
	}
}

// TestParseKiroFrame_CRCFailure verifies that a corrupted message CRC is rejected.
func TestParseKiroFrame_CRCFailure(t *testing.T) {
	payload := []byte(`{"content":{"text":"x"}}`)
	headers := buildKiroEventHeaders("assistantResponseEvent", "event")
	frame := buildKiroFrame(headers, payload)

	// Corrupt the last byte (message CRC)
	frame[len(frame)-1] ^= 0xFF

	_, err := parseKiroFrame(bytes.NewReader(frame))
	if err == nil {
		t.Fatal("expected CRC error, got nil")
	}
}
