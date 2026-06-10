package dsl

type Document struct {
	APIVersion string        `yaml:"apiVersion" json:"apiVersion"`
	Kind       string        `yaml:"kind" json:"kind"`
	Metadata   Metadata      `yaml:"metadata" json:"metadata"`
	Spec       Specification `yaml:"spec" json:"spec"`
}

type Metadata struct {
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Owners      []string          `yaml:"owners,omitempty" json:"owners,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

type Specification struct {
	Inputs  SchemaRef `yaml:"inputs" json:"inputs"`
	Outputs SchemaRef `yaml:"outputs" json:"outputs"`
	Nodes   []Node    `yaml:"nodes" json:"nodes"`
	Edges   []Edge    `yaml:"edges" json:"edges"`
	Start   string    `yaml:"start" json:"start"`
}

type SchemaRef struct {
	Schema map[string]any `yaml:"schema" json:"schema"`
}

type Edge struct {
	From string `yaml:"from" json:"from"`
	To   string `yaml:"to" json:"to"`
}

type Node struct {
	ID       string         `yaml:"id" json:"id"`
	Type     string         `yaml:"type" json:"type"`
	Config   map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
	Retry    *RetryPolicy   `yaml:"retry,omitempty" json:"retry,omitempty"`
	Timeout  string         `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	OnError  string         `yaml:"onError,omitempty" json:"onError,omitempty"`
	Next     []string       `yaml:"next,omitempty" json:"next,omitempty"`
	Cases    []SwitchCase   `yaml:"cases,omitempty" json:"cases,omitempty"`
	Branches []Branch       `yaml:"branches,omitempty" json:"branches,omitempty"`
	Join     string         `yaml:"join,omitempty" json:"join,omitempty"`
}

type RetryPolicy struct {
	MaxAttempts        int     `yaml:"maxAttempts,omitempty" json:"maxAttempts,omitempty"`
	InitialIntervalMS  int64   `yaml:"initialIntervalMs,omitempty" json:"initialIntervalMs,omitempty"`
	BackoffCoefficient float64 `yaml:"backoffCoefficient,omitempty" json:"backoffCoefficient,omitempty"`
}

type SwitchCase struct {
	When    string `yaml:"when,omitempty" json:"when,omitempty"`
	Default bool   `yaml:"default,omitempty" json:"default,omitempty"`
	To      string `yaml:"to" json:"to"`
}

type Branch struct {
	To string `yaml:"to" json:"to"`
}

type CompiledGraph struct {
	SchemaVersion int                     `json:"schemaVersion"`
	AgentName     string                  `json:"agentName"`
	Start         string                  `json:"start"`
	Nodes         map[string]CompiledNode `json:"nodes"`
}

type CompiledNode struct {
	Type     string         `json:"type"`
	Config   map[string]any `json:"config,omitempty"`
	Retry    *RetryPolicy   `json:"retry,omitempty"`
	Timeout  string         `json:"timeout,omitempty"`
	OnError  string         `json:"onError,omitempty"`
	Next     []string       `json:"next"`
	Cases    []SwitchCase   `json:"cases,omitempty"`
	Branches []Branch       `json:"branches,omitempty"`
	Join     string         `json:"join,omitempty"`
}
