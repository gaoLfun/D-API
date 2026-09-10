package gateway

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/gaoLfun/dapi/internal/core"
)

func parseUsage(body []byte) core.Usage {
	return parseUsageWithProtocol(body, "")
}

func parseUsageWithProtocol(body []byte, protocol string) core.Usage {
	var payload struct {
		Usage    json.RawMessage `json:"usage"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
		Message struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return core.Usage{}
	}
	if len(payload.Usage) == 0 {
		payload.Usage = payload.Response.Usage
	}
	if len(payload.Usage) == 0 {
		payload.Usage = payload.Message.Usage
	}
	if len(payload.Usage) == 0 {
		return core.Usage{}
	}
	return parseUsageObjectForProtocol(payload.Usage, protocol)
}

type usageFields struct {
	Input          *int64 `json:"input_tokens"`
	Output         *int64 `json:"output_tokens"`
	Prompt         *int64 `json:"prompt_tokens"`
	Completion     *int64 `json:"completion_tokens"`
	Cached         *int64 `json:"cached_input_tokens"`
	CacheRead      *int64 `json:"cache_read_input_tokens"`
	CacheCreation  *int64 `json:"cache_creation_input_tokens"`
	PromptCacheHit *int64 `json:"prompt_cache_hit_tokens"`
	InputDetails   struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	PromptDetails struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func parseUsageObject(body []byte) core.Usage {
	return parseUsageObjectForProtocol(body, "")
}

func parseUsageObjectForProtocol(body []byte, protocol string) core.Usage {
	var fields usageFields
	if json.Unmarshal(body, &fields) != nil {
		return core.Usage{}
	}
	usage := core.Usage{InputTokens: fields.Input, OutputTokens: fields.Output, CachedInputTokens: fields.Cached, CacheCreationInputTokens: fields.CacheCreation}
	if usage.InputTokens == nil {
		usage.InputTokens = fields.Prompt
	}
	if usage.OutputTokens == nil {
		usage.OutputTokens = fields.Completion
	}
	if usage.CachedInputTokens == nil {
		usage.CachedInputTokens = fields.CacheRead
	}
	if usage.CachedInputTokens == nil {
		usage.CachedInputTokens = fields.InputDetails.Cached
	}
	if usage.CachedInputTokens == nil {
		usage.CachedInputTokens = fields.PromptDetails.Cached
	}
	if usage.CachedInputTokens == nil && protocol != core.ProtocolMessages {
		usage.CachedInputTokens = fields.PromptCacheHit
	}
	return normalizeUsageForProtocol(usage, protocol)
}

func normalizeUsage(usage core.Usage) core.Usage {
	return normalizeUsageForProtocol(usage, "")
}

func normalizeUsageForProtocol(usage core.Usage, protocol string) core.Usage {
	if usage.InputTokens != nil {
		uncached := *usage.InputTokens
		if protocol == core.ProtocolMessages {
			usage.BillableInputTokens = usage.InputTokens
			if usage.CacheCreationInputTokens != nil {
				uncached += *usage.CacheCreationInputTokens
			}
		} else if usage.CachedInputTokens != nil {
			uncached -= *usage.CachedInputTokens
		}
		if uncached < 0 {
			uncached = 0
		}
		usage.UncachedInputTokens = &uncached
	}
	return usage
}

type sseUsageParser struct {
	pending           []byte
	eventData         []byte
	eventDiscard      bool
	afterCR           bool
	started           bool
	discard           bool
	usage             core.Usage
	protocol          string
	textSeen          bool
	failed            bool
	completed         bool
	sawEvent          bool
	requireCompletion bool
	eventType         string
}

const maxSSEEventBytes = 1 << 20

func (p *sseUsageParser) Feed(content []byte) {
	for len(content) > 0 {
		if p.afterCR {
			p.afterCR = false
			if content[0] == '\n' {
				content = content[1:]
				continue
			}
		}
		newline := bytes.IndexAny(content, "\r\n")
		if newline < 0 {
			p.append(content)
			return
		}
		p.append(content[:newline])
		if !p.discard {
			p.parseLine(p.pending)
		} else {
			p.eventDiscard = true
		}
		p.pending = p.pending[:0]
		p.discard = false
		p.afterCR = content[newline] == '\r'
		content = content[newline+1:]
	}
}

func (p *sseUsageParser) append(content []byte) {
	if p.discard {
		return
	}
	if len(p.pending)+len(content) > maxSSEEventBytes {
		p.pending = p.pending[:0]
		p.discard = true
		return
	}
	p.pending = append(p.pending, content...)
}

func (p *sseUsageParser) parseLine(line []byte) {
	if !p.started {
		line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
		p.started = true
	}
	if len(line) == 0 {
		p.dispatch()
		p.eventData = p.eventData[:0]
		p.eventType = ""
		p.eventDiscard = false
		return
	}
	field, value, _ := bytes.Cut(line, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	switch string(field) {
	case "event":
		p.eventType = string(value)
	case "data":
		if p.eventDiscard {
			return
		}
		if len(p.eventData)+len(value)+1 > maxSSEEventBytes {
			p.eventData = p.eventData[:0]
			p.eventDiscard = true
			return
		}
		p.eventData = append(p.eventData, value...)
		p.eventData = append(p.eventData, '\n')
	}
}

func (p *sseUsageParser) dispatch() {
	if p.eventDiscard || len(p.eventData) == 0 {
		return
	}
	data := bytes.TrimSpace(p.eventData)
	if bytes.Equal(data, []byte("[DONE]")) {
		p.completed = true
		return
	}

	var event struct {
		Type     string          `json:"type"`
		Delta    json.RawMessage `json:"delta"`
		Usage    json.RawMessage `json:"usage"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
		Message struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"message"`
		Choices []struct {
			Text  string `json:"text"`
			Delta struct {
				Content json.RawMessage `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &event) != nil {
		return
	}
	p.sawEvent = true
	if event.Type == "" {
		event.Type = p.eventType
	}
	switch event.Type {
	case "error", "response.failed", "response.incomplete":
		p.failed = true
	case "response.completed", "message_stop":
		p.completed = true
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &envelope) == nil && len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		p.failed = true
	}
	for _, raw := range []json.RawMessage{event.Usage, event.Response.Usage, event.Message.Usage} {
		if len(raw) > 0 {
			p.merge(parseUsageObjectForProtocol(raw, p.protocol))
		}
	}
	if eventHasText(event.Type, event.Delta, event.Choices, p.protocol) {
		p.textSeen = true
	}
}

func eventHasText(eventType string, delta json.RawMessage, choices []struct {
	Text  string `json:"text"`
	Delta struct {
		Content json.RawMessage `json:"content"`
	} `json:"delta"`
}, protocol string) bool {
	switch protocol {
	case core.ProtocolResponses:
		return eventType == "response.output_text.delta" && rawString(delta) != ""
	case core.ProtocolMessages:
		if eventType != "content_block_delta" {
			return false
		}
		var value struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		return json.Unmarshal(delta, &value) == nil && value.Type == "text_delta" && value.Text != ""
	case core.ProtocolChat:
		for _, choice := range choices {
			if rawHasText(choice.Delta.Content) || choice.Text != "" {
				return true
			}
		}
	}
	return false
}

func rawString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func rawHasText(raw json.RawMessage) bool {
	if rawString(raw) != "" {
		return true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, part := range parts {
		if (part.Type == "text" || part.Type == "output_text" || part.Type == "") && part.Text != "" {
			return true
		}
	}
	return false
}

func (p *sseUsageParser) merge(usage core.Usage) {
	if usage.InputTokens != nil {
		p.usage.InputTokens = usage.InputTokens
	}
	if usage.OutputTokens != nil {
		p.usage.OutputTokens = usage.OutputTokens
	}
	if usage.CachedInputTokens != nil {
		p.usage.CachedInputTokens = usage.CachedInputTokens
	}
	if usage.CacheCreationInputTokens != nil {
		p.usage.CacheCreationInputTokens = usage.CacheCreationInputTokens
	}
	if usage.UncachedInputTokens != nil {
		p.usage.UncachedInputTokens = usage.UncachedInputTokens
	}
}

func (p *sseUsageParser) Usage() core.Usage {
	// Only blank-line-terminated events contribute usage or completion. EOF
	// and diagnostic snapshots must not dispatch an unfinished event.
	return normalizeUsageForProtocol(p.usage, p.protocol)
}

func (p *sseUsageParser) HasText() bool { return p.textSeen }

func (p *sseUsageParser) StreamError() error {
	if p.failed {
		return errors.New("upstream stream reported failure")
	}

	if (p.requireCompletion || p.sawEvent && p.protocol != "") && !p.completed {
		return errors.New("upstream stream ended before completion")
	}
	return nil
}
