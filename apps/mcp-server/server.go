package mcpserver

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"tidalbridge/packages/protocol"
)

// Requester is the host API: a direct client or a reconnecting one.
type Requester interface {
	Request(ctx context.Context, method, path string, input, output any) error
}
type Empty struct{}
type SyncInput struct {
	Workspace string `json:"workspace"`
	DeviceID  string `json:"device_id"`
}
type MatrixInput struct {
	URL       string              `json:"url"`
	Viewports []protocol.Viewport `json:"viewports"`
	DeviceID  string              `json:"device_id,omitempty"`
}
type JobID struct {
	ID string `json:"id" jsonschema:"Tidal Bridge job ID"`
}
type RunInput struct {
	Argv                []string          `json:"argv" jsonschema:"Command and exact argument array; no inferred shell parsing"`
	Workspace           string            `json:"workspace,omitempty" jsonschema:"Absolute host project directory to synchronize"`
	TimeoutSeconds      int               `json:"timeout_seconds,omitempty"`
	EstimatedDurationMS float64           `json:"estimated_duration_ms,omitempty"`
	ForceLocal          bool              `json:"force_local,omitempty"`
	ForceRemote         bool              `json:"force_remote,omitempty"`
	Idempotent          bool              `json:"idempotent,omitempty" jsonschema:"Explicit guarantee that replay is safe"`
	Provision           bool              `json:"provision,omitempty" jsonschema:"Approve project-scoped dependency provisioning with network access"`
	RuntimeRequirements map[string]string `json:"runtime_requirements,omitempty"`
	ExpectedOutputs     []string          `json:"expected_outputs,omitempty"`
}

func spec(in RunInput) protocol.JobSpec {
	return protocol.JobSpec{Argv: in.Argv, Workspace: in.Workspace, TimeoutSeconds: in.TimeoutSeconds, EstimatedDurationMS: in.EstimatedDurationMS, ExpectedOutputs: in.ExpectedOutputs, Requirements: protocol.Requirements{Runtimes: in.RuntimeRequirements}, Policy: protocol.Policy{LocalFallback: true, Idempotent: in.Idempotent, Retryable: in.Idempotent, ForceLocal: in.ForceLocal, ForceRemote: in.ForceRemote, Provision: in.Provision}}
}
func Server(client Requester) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "tidalbridge", Version: protocol.WorkerVersion}, nil)
	status := func(ctx context.Context, _ *mcp.CallToolRequest, _ Empty) (*mcp.CallToolResult, any, error) {
		var v any
		e := client.Request(ctx, "GET", "/v1/status", nil, &v)
		if e != nil {
			return nil, nil, fmt.Errorf("Tidal Bridge unavailable; continue locally: %w", e)
		}
		return nil, v, nil
	}
	mcp.AddTool(s, &mcp.Tool{Name: "tidalbridge_status", Description: "Inspect host pressure, worker readiness, queue and recent routing."}, status)
	mcp.AddTool(s, &mcp.Tool{Name: "tidalbridge_capabilities", Description: "Inspect factual device capabilities, profiles, calibration and simulated labels."}, status)
	mcp.AddTool(s, &mcp.Tool{Name: "tidalbridge_run", Description: "Submit a trusted developer job. Returns a job ID; poll job_status. Compatible beneficial work runs remotely, otherwise locally."}, func(ctx context.Context, _ *mcp.CallToolRequest, in RunInput) (*mcp.CallToolResult, any, error) {
		var v any
		e := client.Request(ctx, "POST", "/v1/jobs", spec(in), &v)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "tidalbridge_run_profile", Description: "Submit Python/Node/Git work with explicit runtime constraints and optional approved provisioning."}, func(ctx context.Context, _ *mcp.CallToolRequest, in RunInput) (*mcp.CallToolResult, any, error) {
		var v any
		e := client.Request(ctx, "POST", "/v1/jobs", spec(in), &v)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "tidalbridge_explain_route", Description: "Preview compatibility, transfer costs, pressure and expected benefit without executing."}, func(ctx context.Context, _ *mcp.CallToolRequest, in RunInput) (*mcp.CallToolResult, any, error) {
		var v any
		e := client.Request(ctx, "POST", "/v1/explain", spec(in), &v)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "tidalbridge_job_status", Description: "Get job state, attempts, exit code, output and route explanation."}, func(ctx context.Context, _ *mcp.CallToolRequest, in JobID) (*mcp.CallToolResult, any, error) {
		var v any
		e := client.Request(ctx, "GET", "/v1/jobs/"+in.ID, nil, &v)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "tidalbridge_cancel_job", Description: "Cancel a queued or active job and its process tree."}, func(ctx context.Context, _ *mcp.CallToolRequest, in JobID) (*mcp.CallToolResult, any, error) {
		var v any
		e := client.Request(ctx, "POST", "/v1/jobs/"+in.ID+"/cancel", Empty{}, &v)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "tidalbridge_sync_workspace", Description: "Incrementally synchronize a secret-filtered immutable workspace snapshot to an approved worker."}, func(ctx context.Context, _ *mcp.CallToolRequest, in SyncInput) (*mcp.CallToolResult, any, error) {
		var v any
		e := client.Request(ctx, "POST", "/v1/sync", in, &v)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "tidalbridge_render_matrix", Description: "Queue up to 16 viewport captures on real Android Chrome workers. Results are explicitly Tier A, not Windows desktop fidelity."}, func(ctx context.Context, _ *mcp.CallToolRequest, in MatrixInput) (*mcp.CallToolResult, any, error) {
		if len(in.Viewports) < 1 || len(in.Viewports) > 16 {
			return nil, nil, fmt.Errorf("provide 1–16 viewports")
		}
		jobs := []any{}
		for _, viewport := range in.Viewports {
			spec := protocol.JobSpec{Render: &protocol.RenderSpec{URL: in.URL, Viewport: viewport}, TimeoutSeconds: 90, EstimatedDurationMS: 10000, Policy: protocol.Policy{ForceRemote: true, DeviceID: in.DeviceID}}
			var v any
			if e := client.Request(ctx, "POST", "/v1/jobs", spec, &v); e != nil {
				return nil, nil, e
			}
			jobs = append(jobs, v)
		}
		return nil, map[string]any{"jobs": jobs}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "tidalbridge_benchmark", Description: "Queue a short bounded Python CPU benchmark on local or a selected worker. Inspect attempts and never treat simulated workers as physical relief."}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		DeviceID string `json:"device_id,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		spec := protocol.JobSpec{Argv: []string{"python", "-c", "print(sum(i*i for i in range(1000000)))"}, TimeoutSeconds: 30, Policy: protocol.Policy{ForceLocal: in.DeviceID == "", ForceRemote: in.DeviceID != "", DeviceID: in.DeviceID, Idempotent: true, Retryable: true}}
		var v any
		e := client.Request(ctx, "POST", "/v1/jobs", spec, &v)
		return nil, v, e
	})
	return s
}
func Run(ctx context.Context, client Requester) error {
	return Server(client).Run(ctx, &mcp.StdioTransport{})
}
