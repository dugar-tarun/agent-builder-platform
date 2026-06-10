package system

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/dugar-tarun/agent-builder-platform/internal/dsl"
	"github.com/dugar-tarun/agent-builder-platform/internal/engine"
	"github.com/dugar-tarun/agent-builder-platform/internal/storage/pg"
)

func (a *Activities) AppendEvent(ctx context.Context, runID string, seq int64, nodeID, eventType string, payload []byte, meta []byte) error {
	runUUID, err := uuid.Parse(runID)
	if err != nil {
		return err
	}

	var payloadRaw json.RawMessage
	if len(payload) > 0 {
		payloadRaw = json.RawMessage(payload)
	}
	var metaRaw json.RawMessage
	if len(meta) > 0 {
		metaRaw = json.RawMessage(meta)
	}

	return a.Store.AppendEvent(ctx, pg.Event{
		RunID:     runUUID,
		Seq:       seq,
		NodeID:    nodeID,
		EventType: eventType,
		Payload:   payloadRaw,
		Meta:      metaRaw,
	})
}

func (a *Activities) EvaluateSwitch(_ context.Context, _ string, cases []dsl.SwitchCase, evalCtx engine.EvalContext) (string, error) {
	for _, c := range cases {
		if c.Default {
			return c.To, nil
		}
		ok, err := engine.EvalCondition(evalCtx, c.When)
		if err != nil {
			return "", err
		}
		if ok {
			return c.To, nil
		}
	}
	return "", fmt.Errorf("no switch case matched")
}

func (a *Activities) ExecuteLLM(_ context.Context, _ string, config map[string]any, evalCtx engine.EvalContext) ([]byte, error) {
	renderedAny, err := engine.RenderValue(evalCtx, config)
	if err != nil {
		return nil, err
	}
	rendered, _ := renderedAny.(map[string]any)

	provider := toString(rendered["provider"])
	model := toString(rendered["model"])
	systemPrompt := toString(rendered["systemPrompt"])
	userPrompt := toString(rendered["userPrompt"])

	output := map[string]any{
		"provider": provider,
		"model":    model,
	}
	if mock, ok := rendered["mockResponse"]; ok {
		output["output"] = mock
	} else {
		output["output"] = map[string]any{
			"text": fmt.Sprintf("[mock:%s] %s", providerOrDefault(provider), userPrompt),
		}
	}
	if systemPrompt != "" {
		output["systemPrompt"] = systemPrompt
	}
	return json.Marshal(output)
}

func (a *Activities) ExecuteHTTP(ctx context.Context, _ string, config map[string]any, evalCtx engine.EvalContext) ([]byte, error) {
	renderedAny, err := engine.RenderValue(evalCtx, config)
	if err != nil {
		return nil, err
	}
	rendered, _ := renderedAny.(map[string]any)

	method := strings.ToUpper(toString(rendered["method"]))
	if method == "" {
		method = http.MethodGet
	}
	url := toString(rendered["url"])
	if url == "" {
		return nil, fmt.Errorf("http.url is required")
	}

	var bodyReader io.Reader
	if body, ok := rendered["body"]; ok && body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = strings.NewReader(string(bodyBytes))
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	if headers, ok := rendered["headers"].(map[string]any); ok {
		for key, value := range headers {
			req.Header.Set(key, toString(value))
		}
	}
	if bodyReader != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if expect, ok := rendered["expectStatus"].([]any); ok && len(expect) > 0 {
		matched := false
		for _, raw := range expect {
			if int(toFloat(raw)) == resp.StatusCode {
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
	}

	var parsedBody any
	if len(bodyBytes) == 0 {
		parsedBody = nil
	} else if err := json.Unmarshal(bodyBytes, &parsedBody); err != nil {
		parsedBody = string(bodyBytes)
	}

	headersOut := map[string]string{}
	for k, v := range resp.Header {
		if len(v) > 0 {
			headersOut[k] = v[0]
		}
	}

	return json.Marshal(map[string]any{
		"status":  resp.StatusCode,
		"headers": headersOut,
		"body":    parsedBody,
	})
}

func (a *Activities) ExecuteDB(ctx context.Context, _ string, config map[string]any, evalCtx engine.EvalContext) ([]byte, error) {
	renderedAny, err := engine.RenderValue(evalCtx, config)
	if err != nil {
		return nil, err
	}
	rendered, _ := renderedAny.(map[string]any)

	source := toString(rendered["source"])
	query := toString(rendered["query"])
	if source == "" || query == "" {
		return nil, fmt.Errorf("db.source and db.query are required")
	}

	dsnEnvVar := "AGENT_DB_" + normalizeKey(source) + "_DSN"
	dsn := os.Getenv(dsnEnvVar)
	if dsn == "" {
		return nil, fmt.Errorf("missing db dsn env var: %s", dsnEnvVar)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	params := []any{}
	if rawParams, ok := rendered["params"].([]any); ok {
		params = rawParams
	}

	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		scanTargets := make([]any, len(columns))
		for i := range values {
			scanTargets[i] = &values[i]
		}
		if err := rows.Scan(scanTargets...); err != nil {
			return nil, err
		}

		rowOut := map[string]any{}
		for i, col := range columns {
			rowOut[col] = normalizeDBValue(values[i])
		}
		results = append(results, rowOut)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	resultMode := strings.ToLower(toString(rendered["resultMode"]))
	if resultMode == "single" {
		if len(results) == 0 {
			return json.Marshal(nil)
		}
		return json.Marshal(results[0])
	}
	return json.Marshal(results)
}

func (a *Activities) ExecuteTransform(_ context.Context, _ string, config map[string]any, evalCtx engine.EvalContext) ([]byte, error) {
	renderedAny, err := engine.RenderValue(evalCtx, config)
	if err != nil {
		return nil, err
	}
	rendered, _ := renderedAny.(map[string]any)
	expression := toString(rendered["expression"])
	if expression == "" {
		return nil, fmt.Errorf("transform.expression is required")
	}

	value, err := engine.RenderString(evalCtx, expression)
	if err != nil {
		return nil, err
	}

	if text, ok := value.(string); ok {
		var parsed any
		if json.Unmarshal([]byte(text), &parsed) == nil {
			return json.Marshal(parsed)
		}
	}
	return json.Marshal(value)
}

func providerOrDefault(provider string) string {
	if provider == "" {
		return "llm"
	}
	return provider
}

func toString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func toFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func normalizeKey(input string) string {
	up := strings.ToUpper(input)
	var b strings.Builder
	for _, r := range up {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteRune('_')
	}
	return strings.Trim(b.String(), "_")
}

func normalizeDBValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return v
	}
}
