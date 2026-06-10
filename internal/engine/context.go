package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type EvalContext struct {
	Inputs map[string]any `json:"inputs"`
	Nodes  map[string]any `json:"nodes"`
	Run    map[string]any `json:"run"`
}

func NewEvalContext(inputs map[string]any, runID string) EvalContext {
	return EvalContext{
		Inputs: inputs,
		Nodes:  map[string]any{},
		Run: map[string]any{
			"id": runID,
		},
	}
}

func ResolvePath(ctx EvalContext, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}

	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return nil, false
	}

	switch parts[0] {
	case "inputs":
		return resolveFrom(ctx.Inputs, parts[1:])
	case "run":
		return resolveFrom(ctx.Run, parts[1:])
	case "nodes":
		if len(parts) < 3 {
			return nil, false
		}
		nodeID := parts[1]
		if parts[2] != "output" {
			return nil, false
		}
		root, ok := ctx.Nodes[nodeID]
		if !ok {
			return nil, false
		}
		return resolveFrom(root, parts[3:])
	case "secrets":
		if len(parts) != 2 {
			return nil, false
		}
		key := "AGENT_SECRET_" + strings.ToUpper(strings.ReplaceAll(parts[1], "-", "_"))
		val := os.Getenv(key)
		if val == "" {
			return nil, false
		}
		return val, true
	default:
		return nil, false
	}
}

func RenderValue(ctx EvalContext, value any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, inner := range v {
			rendered, err := RenderValue(ctx, inner)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, inner := range v {
			rendered, err := RenderValue(ctx, inner)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	case string:
		return RenderString(ctx, v)
	default:
		return value, nil
	}
}

func RenderString(ctx EvalContext, text string) (any, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}

	// Typed mode: the full string is one expression.
	if strings.HasPrefix(text, "{{") && strings.HasSuffix(text, "}}") && strings.Count(text, "{{") == 1 && strings.Count(text, "}}") == 1 {
		expr := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "{{"), "}}"))
		return evalExpr(ctx, expr)
	}

	// String mode: interpolate any expression snippets.
	var result strings.Builder
	remaining := text
	for {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			result.WriteString(remaining)
			break
		}
		end := strings.Index(remaining[start+2:], "}}")
		if end < 0 {
			result.WriteString(remaining)
			break
		}
		end = start + 2 + end

		result.WriteString(remaining[:start])
		expr := strings.TrimSpace(remaining[start+2 : end])
		value, err := evalExpr(ctx, expr)
		if err != nil {
			return nil, err
		}
		result.WriteString(fmt.Sprint(value))
		remaining = remaining[end+2:]
	}
	return result.String(), nil
}

func EvalCondition(ctx EvalContext, expr string) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false, nil
	}

	ops := []string{">=", "<=", "==", "!=", ">", "<"}
	for _, op := range ops {
		idx := strings.Index(expr, op)
		if idx <= 0 {
			continue
		}
		left := strings.TrimSpace(expr[:idx])
		right := strings.TrimSpace(expr[idx+len(op):])
		lv, err := evalExpr(ctx, left)
		if err != nil {
			return false, err
		}
		rv, err := evalExpr(ctx, right)
		if err != nil {
			return false, err
		}
		return compare(op, lv, rv), nil
	}

	value, err := evalExpr(ctx, expr)
	if err != nil {
		return false, err
	}
	return truthy(value), nil
}

func evalExpr(ctx EvalContext, expr string) (any, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", nil
	}

	// Simple pipeline support: <path> | toJson
	if strings.Contains(expr, "|") {
		parts := strings.Split(expr, "|")
		base := strings.TrimSpace(parts[0])
		value, err := evalExpr(ctx, base)
		if err != nil {
			return nil, err
		}
		for _, fn := range parts[1:] {
			fn = strings.TrimSpace(fn)
			switch fn {
			case "toJson":
				b, err := json.Marshal(value)
				if err != nil {
					return nil, err
				}
				value = string(b)
			default:
				return nil, fmt.Errorf("unsupported function: %s", fn)
			}
		}
		return value, nil
	}

	// String literal.
	if (strings.HasPrefix(expr, "\"") && strings.HasSuffix(expr, "\"")) || (strings.HasPrefix(expr, "'") && strings.HasSuffix(expr, "'")) {
		return expr[1 : len(expr)-1], nil
	}

	// bool literal.
	if expr == "true" {
		return true, nil
	}
	if expr == "false" {
		return false, nil
	}

	// number literal.
	if i, err := strconv.ParseInt(expr, 10, 64); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(expr, 64); err == nil {
		return f, nil
	}

	// dotted reference.
	if value, ok := ResolvePath(ctx, expr); ok {
		return value, nil
	}

	return nil, fmt.Errorf("unsupported expression: %s", expr)
}

func resolveFrom(root any, parts []string) (any, bool) {
	cur := root
	for _, part := range parts {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

func compare(op string, left, right any) bool {
	lf, lok := asFloat(left)
	rf, rok := asFloat(right)
	if lok && rok {
		switch op {
		case ">":
			return lf > rf
		case "<":
			return lf < rf
		case ">=":
			return lf >= rf
		case "<=":
			return lf <= rf
		case "==":
			return lf == rf
		case "!=":
			return lf != rf
		}
	}

	ls := fmt.Sprint(left)
	rs := fmt.Sprint(right)
	switch op {
	case "==":
		return ls == rs
	case "!=":
		return ls != rs
	case ">":
		return ls > rs
	case "<":
		return ls < rs
	case ">=":
		return ls >= rs
	case "<=":
		return ls <= rs
	default:
		return false
	}
}

func asFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return strings.TrimSpace(v) != ""
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return true
	}
}
