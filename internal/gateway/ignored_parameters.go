package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
)

// IgnoredParametersHeader lists the request parameters the gateway recognised,
// accepted, and could not honour.
//
// The gateway reaches every backend through providers.GenerateRequest, which
// carries no sampling knobs at all -- no temperature, no top_p, no seed. That is
// a property of the shared provider contract rather than of any one adapter, so
// these parameters cannot be forwarded no matter which model serves the call.
//
// Both obvious answers to that are wrong. Rejecting the request was the earlier
// behaviour: the standard OpenAI clients set temperature on nearly every call,
// so a 400 turns "OpenAI-compatible" into "compatible with clients that send
// nothing optional". Accepting silently is the opposite failure -- a caller that
// sets temperature 0 for determinism is told nothing and quietly gets whatever
// the backend defaults to, which surfaces much later as a reproducibility bug
// with no visible cause.
//
// So the request succeeds and reports what it dropped. A response header is the
// right carrier: clients that are not looking for it ignore it, clients and
// operators that are see it in curl, devtools, and proxy logs, and because
// headers precede the body a streaming response can carry it just as well as a
// buffered one. Nothing about the documented response schema changes.
const IgnoredParametersHeader = "X-Autoto-Ignored-Parameters"

// setIgnoredParameters must be called before the first write, because a
// ResponseWriter commits its headers as soon as the body starts.
func setIgnoredParameters(w http.ResponseWriter, ignored []string) {
	if len(ignored) == 0 {
		return
	}
	w.Header().Set(IgnoredParametersHeader, strings.Join(ignored, ", "))
}

// jsonPresent reports whether a raw field was supplied at all. An explicit null
// counts as absent: it carries no instruction to drop.
func jsonPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

// chatIgnoredParameters lists, in the order OpenAI documents them, the sampling
// and output controls a caller sent that this gateway cannot pass on.
// parallel_tool_calls is included because the chat path parses it and never
// reads it; the responses path rejects an explicit false instead, which is why
// it is not listed there.
func chatIgnoredParameters(request chatCompletionRequest) []string {
	ignored := make([]string, 0, 9)
	for _, parameter := range []struct {
		name    string
		present bool
	}{
		{"temperature", request.Temperature != nil},
		{"top_p", request.TopP != nil},
		{"n", request.N != nil},
		{"stop", jsonPresent(request.Stop)},
		{"presence_penalty", request.PresencePenalty != nil},
		{"frequency_penalty", request.FrequencyPenalty != nil},
		{"logprobs", request.Logprobs != nil},
		{"seed", request.Seed != nil},
		{"parallel_tool_calls", request.ParallelToolCalls != nil},
	} {
		if parameter.present {
			ignored = append(ignored, parameter.name)
		}
	}
	// "priority" is honoured as fast mode and "auto"/"default" ask for what the
	// gateway already does, so only an unrecognised tier is being dropped.
	switch strings.ToLower(strings.TrimSpace(request.ServiceTier)) {
	case "", "auto", "default", "priority":
	default:
		ignored = append(ignored, "service_tier")
	}
	return ignored
}

// anthropicIgnoredParameters mirrors chatIgnoredParameters for the Messages
// protocol. tool_choice is listed here and not on the chat path because the
// chat path converts it.
func anthropicIgnoredParameters(request anthropicMessagesRequest) []string {
	ignored := make([]string, 0, 6)
	for _, parameter := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"temperature", request.Temperature},
		{"top_p", request.TopP},
		{"top_k", request.TopK},
		{"stop_sequences", request.StopSequences},
		{"tool_choice", request.ToolChoice},
		{"metadata", request.Metadata},
	} {
		if jsonPresent(parameter.raw) {
			ignored = append(ignored, parameter.name)
		}
	}
	return ignored
}
