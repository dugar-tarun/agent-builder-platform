package dsl

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var agentNamePattern = regexp.MustCompile("^[a-z0-9-]{1,64}$")

type ValidationError struct {
	Path  string `json:"path"`
	Issue string `json:"issue"`
}

func (v ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", v.Path, v.Issue)
}

func ParseAndCompile(rawYAML []byte) (*Document, *CompiledGraph, []byte, error) {
	var doc Document
	if err := yaml.Unmarshal(rawYAML, &doc); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid YAML: %w", err)
	}

	if err := validateDocument(&doc); err != nil {
		return nil, nil, nil, err
	}

	graph, err := compileDocument(&doc)
	if err != nil {
		return nil, nil, nil, err
	}

	normalized, err := json.Marshal(graph)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal compiled graph: %w", err)
	}

	sum := sha256.Sum256(normalized)
	return &doc, graph, sum[:], nil
}

func validateDocument(doc *Document) error {
	if doc.APIVersion != "agents.v1" {
		return ValidationError{Path: "apiVersion", Issue: "must be agents.v1"}
	}
	if doc.Kind != "Agent" {
		return ValidationError{Path: "kind", Issue: "must be Agent"}
	}
	if !agentNamePattern.MatchString(doc.Metadata.Name) {
		return ValidationError{Path: "metadata.name", Issue: "must match [a-z0-9-]{1,64}"}
	}
	if len(doc.Spec.Nodes) == 0 {
		return ValidationError{Path: "spec.nodes", Issue: "must contain at least one node"}
	}

	ids := make(map[string]struct{}, len(doc.Spec.Nodes))
	for i, node := range doc.Spec.Nodes {
		if strings.TrimSpace(node.ID) == "" {
			return ValidationError{Path: fmt.Sprintf("spec.nodes[%d].id", i), Issue: "required"}
		}
		if _, ok := ids[node.ID]; ok {
			return ValidationError{Path: fmt.Sprintf("spec.nodes[%d].id", i), Issue: "must be unique"}
		}
		ids[node.ID] = struct{}{}
		if strings.TrimSpace(node.Type) == "" {
			return ValidationError{Path: fmt.Sprintf("spec.nodes[%d].type", i), Issue: "required"}
		}
	}

	start := doc.Spec.Start
	if start == "" {
		start = doc.Spec.Nodes[0].ID
	}
	if _, ok := ids[start]; !ok {
		return ValidationError{Path: "spec.start", Issue: "must reference an existing node"}
	}

	for i, edge := range doc.Spec.Edges {
		if _, ok := ids[edge.From]; !ok {
			return ValidationError{Path: fmt.Sprintf("spec.edges[%d].from", i), Issue: "unknown node id"}
		}
		if _, ok := ids[edge.To]; !ok {
			return ValidationError{Path: fmt.Sprintf("spec.edges[%d].to", i), Issue: "unknown node id"}
		}
	}

	for i, node := range doc.Spec.Nodes {
		for j, n := range node.Next {
			if _, ok := ids[n]; !ok {
				return ValidationError{Path: fmt.Sprintf("spec.nodes[%d].next[%d]", i, j), Issue: "unknown node id"}
			}
		}

		if node.Type == "control.switch" {
			if len(node.Cases) == 0 {
				return ValidationError{Path: fmt.Sprintf("spec.nodes[%d].cases", i), Issue: "must not be empty"}
			}
			hasDefault := false
			for cIdx, c := range node.Cases {
				if _, ok := ids[c.To]; !ok {
					return ValidationError{Path: fmt.Sprintf("spec.nodes[%d].cases[%d].to", i, cIdx), Issue: "unknown node id"}
				}
				if c.Default {
					hasDefault = true
				}
			}
			if !hasDefault {
				return ValidationError{Path: fmt.Sprintf("spec.nodes[%d].cases", i), Issue: "must include one default case"}
			}
		}

		if node.Type == "control.parallel" {
			if len(node.Branches) == 0 {
				return ValidationError{Path: fmt.Sprintf("spec.nodes[%d].branches", i), Issue: "must not be empty"}
			}
			if len(node.Next) != 1 {
				return ValidationError{Path: fmt.Sprintf("spec.nodes[%d].next", i), Issue: "must contain exactly one continuation node"}
			}
			for bIdx, b := range node.Branches {
				if _, ok := ids[b.To]; !ok {
					return ValidationError{Path: fmt.Sprintf("spec.nodes[%d].branches[%d].to", i, bIdx), Issue: "unknown node id"}
				}
			}
		}
	}

	graph, err := compileDocument(doc)
	if err != nil {
		return err
	}
	if err := validateReachability(graph); err != nil {
		return err
	}
	if err := validateAcyclic(graph); err != nil {
		return err
	}
	return nil
}

func compileDocument(doc *Document) (*CompiledGraph, error) {
	graph := &CompiledGraph{
		SchemaVersion: 1,
		AgentName:     doc.Metadata.Name,
		Start:         doc.Spec.Start,
		Nodes:         make(map[string]CompiledNode, len(doc.Spec.Nodes)),
	}
	if graph.Start == "" {
		graph.Start = doc.Spec.Nodes[0].ID
	}

	explicitEdges := map[string][]string{}
	for _, e := range doc.Spec.Edges {
		explicitEdges[e.From] = append(explicitEdges[e.From], e.To)
	}

	for _, node := range doc.Spec.Nodes {
		compiled := CompiledNode{
			Type:     node.Type,
			Config:   node.Config,
			Retry:    node.Retry,
			Timeout:  node.Timeout,
			OnError:  defaultOnError(node.OnError),
			Cases:    node.Cases,
			Branches: node.Branches,
			Join:     node.Join,
		}
		next := []string{}
		switch node.Type {
		case "control.switch":
			for _, c := range node.Cases {
				next = append(next, c.To)
			}
		case "control.parallel":
			next = append(next, node.Next...)
		default:
			next = append(next, explicitEdges[node.ID]...)
			next = append(next, node.Next...)
		}
		compiled.Next = uniqSorted(next)
		graph.Nodes[node.ID] = compiled
	}

	return graph, nil
}

func validateReachability(graph *CompiledGraph) error {
	visited := map[string]struct{}{}
	var walk func(string)
	walk = func(nodeID string) {
		if _, ok := visited[nodeID]; ok {
			return
		}
		visited[nodeID] = struct{}{}
		node := graph.Nodes[nodeID]
		for _, next := range node.Next {
			walk(next)
		}
	}
	walk(graph.Start)

	if len(visited) == len(graph.Nodes) {
		return nil
	}

	orphaned := make([]string, 0, len(graph.Nodes)-len(visited))
	for id := range graph.Nodes {
		if _, ok := visited[id]; !ok {
			orphaned = append(orphaned, id)
		}
	}
	sort.Strings(orphaned)
	return ValidationError{
		Path:  "spec.nodes",
		Issue: fmt.Sprintf("orphaned nodes: %s", strings.Join(orphaned, ",")),
	}
}

func validateAcyclic(graph *CompiledGraph) error {
	const (
		unseen = iota
		visiting
		done
	)
	state := map[string]int{}
	var cycleErr error

	var dfs func(string)
	dfs = func(nodeID string) {
		if cycleErr != nil {
			return
		}
		switch state[nodeID] {
		case visiting:
			cycleErr = ValidationError{
				Path:  "spec.edges",
				Issue: fmt.Sprintf("cycle detected at node %s", nodeID),
			}
			return
		case done:
			return
		}
		state[nodeID] = visiting
		for _, next := range graph.Nodes[nodeID].Next {
			dfs(next)
		}
		state[nodeID] = done
	}

	for id := range graph.Nodes {
		dfs(id)
	}
	return cycleErr
}

func defaultOnError(value string) string {
	if value == "" {
		return "fail"
	}
	return value
}

func uniqSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func IsValidationError(err error) bool {
	var v ValidationError
	return errors.As(err, &v)
}
