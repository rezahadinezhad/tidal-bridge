package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"tidalbridge/packages/protocol"
	"time"
)

// Execute owns an accepted job until completion. Transport failures never
// trigger a second execution in the caller: an accepted job may still run.
func (c Client) Execute(ctx context.Context, spec protocol.JobSpec, stdout, stderr io.Writer) (int, error) {
	return c.ExecuteObserved(ctx, spec, stdout, stderr, nil)
}
func (c Client) ExecuteObserved(ctx context.Context, spec protocol.JobSpec, stdout, stderr io.Writer, observe func(protocol.Job)) (int, error) {
	var j protocol.Job
	if err := c.Request(ctx, "POST", "/v1/jobs", spec, &j); err != nil {
		return 1, fmt.Errorf("submission failed; execution may have been accepted, do not blindly replay: %w", err)
	}
	if observe != nil {
		observe(j)
	}
	cancelJob := func() {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c.Request(cancelCtx, "POST", "/v1/jobs/"+j.ID+"/cancel", map[string]any{}, nil)
	}
	if !c.Quiet {
		fmt.Fprintln(stderr, "Tidal Bridge job", j.ID)
	}
	offsets := map[string]int64{"stdout": 0, "stderr": 0}
	count := 0
	for {
		if err := c.Request(ctx, "GET", "/v1/jobs/"+j.ID, nil, &j); err != nil {
			if ctx.Err() != nil {
				cancelJob()
				return 130, ctx.Err()
			}
			return 1, fmt.Errorf("job %s may still be running; inspect this ID before resubmitting: %w", j.ID, err)
		}
		if len(j.Attempts) != count {
			offsets["stdout"] = 0
			offsets["stderr"] = 0
			count = len(j.Attempts)
			if !c.Quiet {
				fmt.Fprintln(stderr, "Route:", j.Decision.Explanation)
			}
		}
		for _, stream := range []string{"stdout", "stderr"} {
			for {
				var chunk struct {
					Data string `json:"data_b64"`
					Next int64  `json:"next_offset"`
				}
				if err := c.Request(ctx, "GET", fmt.Sprintf("/v1/jobs/%s/output?stream=%s&offset=%d", j.ID, stream, offsets[stream]), nil, &chunk); err != nil {
					if ctx.Err() != nil {
						cancelJob()
						return 130, ctx.Err()
					}
					return 1, fmt.Errorf("job %s output unavailable; do not duplicate execution: %w", j.ID, err)
				}
				if chunk.Next == offsets[stream] {
					break
				}
				offsets[stream] = chunk.Next
				data, err := base64.StdEncoding.DecodeString(chunk.Data)
				if err != nil {
					return 1, err
				}
				writer := stdout
				if stream == "stderr" {
					writer = stderr
				}
				if _, err = writer.Write(data); err != nil {
					return 1, err
				}
				if len(data) < 65536 {
					break
				}
			}
		}
		if j.Finished != nil {
			if observe != nil {
				observe(j)
			}
			if j.ExitCode != nil {
				return *j.ExitCode, nil
			}
			return 1, fmt.Errorf("%s: %s", j.State, j.Error)
		}
		select {
		case <-ctx.Done():
			cancelJob()
			return 130, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}
