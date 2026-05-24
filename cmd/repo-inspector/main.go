// repo-inspector is an LLM-backed R2 worker that inspects repository files and
// provides structured analysis. It consumes work orders from the event bus,
// reads files, optionally searches, uses a local LLM for analysis, and publishes
// progress and result events.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/patch/agora-os/internal/llm"
	"github.com/patch/agora-os/internal/r2worker"
	"github.com/patch/agora-os/internal/schema"
)

// repoInspectorInputs defines the expected work-order payload.
type repoInspectorInputs struct {
	TargetPaths []string `json:"target_paths,omitempty"` // explicit files to inspect
	Question    string   `json:"question"`               // what to analyze
	SearchQuery string   `json:"search_query,omitempty"` // optional grep query to discover files
}

// inspectionReport is the structured output artifact.
type inspectionReport struct {
	TaskID     string    `json:"task_id"`
	Question   string    `json:"question"`
	FilesRead  []string  `json:"files_read"`
	StartedAt  time.Time `json:"started_at"`
	Analysis   string    `json:"analysis"`
	ModelUsed  string    `json:"model_used"`
}

const systemPrompt = `You are a code inspector. Analyze the provided files and answer the user's question concisely and accurately. Focus on structure, logic, and potential issues. If files are not readable or missing, say so.`

const maxFileSize = 128 * 1024 // 128 KiB per file
const maxFiles = 10

type repoInspectorHandler struct {
	llmClient *llm.Client
}

func newRepoInspectorHandler(opts ...llm.ClientOption) *repoInspectorHandler {
	return &repoInspectorHandler{
		llmClient: llm.NewClient(append([]llm.ClientOption{llm.WithSystemPrompt(systemPrompt)}, opts...)...),
	}
}

func (h *repoInspectorHandler) HandleWork(ctx context.Context, r *r2worker.Runner, order schema.WorkOrder) error {
	var inputs repoInspectorInputs
	if len(order.Inputs) > 0 {
		if err := json.Unmarshal(order.Inputs, &inputs); err != nil {
			r.PublishResult(order.TaskID, schema.WorkStatusFailed, "invalid inputs", nil, nil, err.Error())
			return err
		}
	}

	if inputs.Question == "" {
		r.PublishResult(order.TaskID, schema.WorkStatusFailed, "missing question", nil, nil, "question is required")
		return fmt.Errorf("missing question")
	}

	repoRoot := "."

	// Step 1: discover files (explicit or via search)
	if err := r.CheckBudget(ctx); err != nil {
		r.PublishResult(order.TaskID, schema.WorkStatusFailed, "budget exhausted", nil, nil, err.Error())
		return err
	}
	r.PublishProgress(order.TaskID, "discover", "discovering target files", 1)
	targetFiles, err := h.discoverFiles(ctx, repoRoot, inputs)
	if err != nil {
		r.PublishResult(order.TaskID, schema.WorkStatusFailed, "discovery failed", nil, nil, err.Error())
		return err
	}

	if len(targetFiles) == 0 {
		r.PublishResult(order.TaskID, schema.WorkStatusOK, "no files found to inspect", nil, nil, "")
		return nil
	}

	// Step 2: read files
	if err := r.CheckBudget(ctx); err != nil {
		r.PublishResult(order.TaskID, schema.WorkStatusFailed, "budget exhausted", nil, nil, err.Error())
		return err
	}
	r.PublishProgress(order.TaskID, "read", fmt.Sprintf("reading %d files", len(targetFiles)), 2)
	fileContents := h.readFiles(targetFiles)

	// Step 3: LLM analysis
	if err := r.CheckBudget(ctx); err != nil {
		r.PublishResult(order.TaskID, schema.WorkStatusFailed, "budget exhausted", nil, nil, err.Error())
		return err
	}
	r.PublishProgress(order.TaskID, "analyze", "sending to LLM", 3)
	analysis, err := h.analyze(ctx, inputs.Question, fileContents)
	if err != nil {
		r.PublishResult(order.TaskID, schema.WorkStatusFailed, "LLM analysis failed", nil, nil, err.Error())
		return err
	}

	report := inspectionReport{
		TaskID:    order.TaskID,
		Question:  inputs.Question,
		FilesRead:  targetFiles,
		StartedAt: time.Now(),
		Analysis:  analysis,
		ModelUsed: h.llmClient.Model(),
	}
	reportJSON, _ := json.MarshalIndent(report, "", "  ")
	artifacts := []schema.ArtifactRef{
		{Kind: schema.ArtifactSummary, Text: string(reportJSON)},
		{Kind: schema.ArtifactLog, Text: analysis},
	}

	r.PublishProgress(order.TaskID, "complete", "inspection finished", 4)
	r.PublishResult(order.TaskID, schema.WorkStatusOK, "inspection completed", artifacts, nil, "")
	return nil
}

func (h *repoInspectorHandler) discoverFiles(ctx context.Context, root string, inputs repoInspectorInputs) ([]string, error) {
	var files []string
	seen := make(map[string]bool)

	for _, p := range inputs.TargetPaths {
		var joined string
		if filepath.IsAbs(p) {
			joined = p
		} else {
			joined = filepath.Join(root, p)
		}
		abs, err := filepath.Abs(joined)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if info.IsDir() {
			entries, err := os.ReadDir(abs)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					f := filepath.Join(abs, e.Name())
					if !seen[f] {
						seen[f] = true
						files = append(files, f)
					}
				}
			}
		} else {
			if !seen[abs] {
				seen[abs] = true
				files = append(files, abs)
			}
		}
	}

	if inputs.SearchQuery != "" && len(files) < maxFiles {
		cmd := exec.CommandContext(ctx, "grep", "-r", "-l", "-I", inputs.SearchQuery, root)
		out, err := cmd.Output()
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line != "" && !seen[line] {
					seen[line] = true
					files = append(files, line)
					if len(files) >= maxFiles {
						break
					}
				}
			}
		}
	}

	return files, nil
}

func (h *repoInspectorHandler) readFiles(paths []string) map[string]string {
	out := make(map[string]string)
	for _, p := range paths {
		if len(out) >= maxFiles {
			break
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if info.Size() > maxFileSize {
			out[p] = fmt.Sprintf("<file too large: %d bytes>", info.Size())
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out[p] = string(data)
	}
	return out
}

func (h *repoInspectorHandler) analyze(ctx context.Context, question string, files map[string]string) (string, error) {
	var sb strings.Builder
	sb.WriteString("Question: ")
	sb.WriteString(question)
	sb.WriteString("\n\n")
	for path, content := range files {
		sb.WriteString(fmt.Sprintf("--- %s ---\n", path))
		sb.WriteString(content)
		sb.WriteString("\n\n")
	}

	msgs := []llm.ChatMessage{{Role: "user", Content: sb.String()}}
	resp, err := h.llmClient.ChatCompletion(ctx, msgs)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no completion choices returned")
	}
	return resp.Choices[0].Message.Content, nil
}

func main() {
	log.SetPrefix("repo-inspector: ")

	handler := newRepoInspectorHandler()
	runner, err := r2worker.NewRunner(handler)
	if err != nil {
		log.Fatalf("init runner: %v", err)
	}

	ctx := context.Background()
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("runner exited: %v", err)
	}
}
