// repo-search is a deterministic R2 worker that performs repository search
// operations using find, grep, and git. It consumes work orders from the event
// bus and publishes structured progress and result events.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/patch/agora-os/internal/r2worker"
	"github.com/patch/agora-os/internal/schema"
)

// repoSearchInputs defines the expected work-order payload for repo-search.
type repoSearchInputs struct {
	Query   string   `json:"query"`
	Paths   []string `json:"paths,omitempty"`
	GitArgs []string `json:"git_args,omitempty"` // e.g. ["log", "--oneline", "-5"]
}

// searchResult is a single structured finding from a search tool.
type searchResult struct {
	Tool   string   `json:"tool"`
	Output string   `json:"output"`
	Files  []string `json:"files,omitempty"`
}

// searchReport is the aggregated structured output sent as a result artifact.
type searchReport struct {
	TaskID    string         `json:"task_id"`
	Query     string         `json:"query"`
	StartedAt time.Time      `json:"started_at"`
	Results   []searchResult `json:"results"`
}

type repoSearchHandler struct{}

func (repoSearchHandler) HandleWork(ctx context.Context, r *r2worker.Runner, order schema.WorkOrder) error {
	var inputs repoSearchInputs
	if len(order.Inputs) > 0 {
		if err := json.Unmarshal(order.Inputs, &inputs); err != nil {
			r.PublishResult(order.TaskID, schema.WorkStatusFailed, "invalid inputs", nil, nil, err.Error())
			return err
		}
	}

	repoRoot := "."
	if len(inputs.Paths) > 0 {
		repoRoot = inputs.Paths[0]
	}

	report := searchReport{
		TaskID:    order.TaskID,
		Query:     inputs.Query,
		StartedAt: time.Now(),
	}

	// Step 1: find
	if err := r.CheckBudget(ctx); err != nil {
		r.PublishResult(order.TaskID, schema.WorkStatusFailed, "budget exhausted", nil, nil, err.Error())
		return err
	}
	r.PublishProgress(order.TaskID, "search", "running find", 1)
	findRes := runFind(ctx, repoRoot, inputs.Query)
	report.Results = append(report.Results, findRes)

	// Step 2: grep
	if err := r.CheckBudget(ctx); err != nil {
		r.PublishResult(order.TaskID, schema.WorkStatusFailed, "budget exhausted", nil, nil, err.Error())
		return err
	}
	r.PublishProgress(order.TaskID, "search", "running grep", 2)
	grepRes := runGrep(ctx, repoRoot, inputs.Query)
	report.Results = append(report.Results, grepRes)

	// Step 3: git (optional)
	if len(inputs.GitArgs) > 0 {
		if err := r.CheckBudget(ctx); err != nil {
			r.PublishResult(order.TaskID, schema.WorkStatusFailed, "budget exhausted", nil, nil, err.Error())
			return err
		}
		r.PublishProgress(order.TaskID, "search", "running git", 3)
		gitRes := runGit(ctx, repoRoot, inputs.GitArgs)
		report.Results = append(report.Results, gitRes)
	}

	// Publish final result with structured artifact
	r.PublishProgress(order.TaskID, "complete", "search finished", 4)
	reportJSON, _ := json.MarshalIndent(report, "", "  ")
	artifacts := []schema.ArtifactRef{
		{Kind: schema.ArtifactSummary, Text: string(reportJSON)},
	}
	r.PublishResult(order.TaskID, schema.WorkStatusOK, "search completed", artifacts, nil, "")
	return nil
}

func runFind(ctx context.Context, root, query string) searchResult {
	nameArg := "*" + query + "*"
	cmd := exec.CommandContext(ctx, "find", root, "-type", "f", "-name", nameArg)
	out, err := cmd.Output()
	res := searchResult{Tool: "find"}
	if err != nil {
		res.Output = fmt.Sprintf("find error: %v", err)
		return res
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			res.Files = append(res.Files, line)
		}
	}
	res.Output = string(out)
	return res
}

func runGrep(ctx context.Context, root, query string) searchResult {
	res := searchResult{Tool: "grep"}
	if query == "" {
		res.Output = "grep skipped: empty query"
		return res
	}
	cmd := exec.CommandContext(ctx, "grep", "-r", "-l", "-I", query, root)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			res.Output = "no matches"
			return res
		}
		res.Output = fmt.Sprintf("grep error: %v", err)
		return res
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			res.Files = append(res.Files, line)
		}
	}
	res.Output = string(out)
	return res
}

func runGit(ctx context.Context, root string, args []string) searchResult {
	res := searchResult{Tool: "git"}
	if len(args) == 0 {
		args = []string{"status", "--short"}
	}
	cmdArgs := append([]string{"-C", root}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		res.Output = fmt.Sprintf("git error: %v", err)
		return res
	}
	res.Output = string(out)
	return res
}

func main() {
	log.SetPrefix("repo-search: ")

	runner, err := r2worker.NewRunner(repoSearchHandler{})
	if err != nil {
		log.Fatalf("init runner: %v", err)
	}

	ctx := context.Background()
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("runner exited: %v", err)
	}
}
