package tool

import (
	"context"
	"fmt"
	"time"
)

// ToolRuntime is an immutable tool catalog and execution service.
type ToolRuntime struct {
	tools      map[string]registeredTool
	specs      []Spec
	middleware []ExecuteMiddleware
}

// Specs returns tool descriptors in registration order. Returned schemas are
// independent copies and may be modified by the caller.
func (r *ToolRuntime) Specs() []Spec {
	if r == nil {
		return nil
	}
	specs := make([]Spec, len(r.specs))
	for index, spec := range r.specs {
		specs[index] = cloneSpec(spec)
	}
	return specs
}

// Execute validates, resolves, and executes one Tool Call.
func (r *ToolRuntime) Execute(ctx context.Context, call Call) (Result, error) {
	if r == nil {
		return Result{}, ErrToolNotFound
	}
	if ctx == nil {
		return Result{}, ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if call.Name == "" {
		return Result{}, ErrEmptyToolName
	}
	if !isJSONObject(call.Arguments) {
		return Result{}, fmt.Errorf("%w: %s", ErrInvalidArguments, call.Name)
	}
	registered, exists := r.tools[call.Name]
	if !exists {
		return Result{}, fmt.Errorf("%w: %s", ErrToolNotFound, call.Name)
	}

	startTime := time.Now().UnixMilli()
	next := ExecuteNext(func(nextCtx context.Context, nextCall Call) (Result, error) {
		result, err := registered.tool.Execute(nextCtx, cloneCallArguments(nextCall))
		if err != nil {
			return Result{}, fmt.Errorf("tool: execute %q: %w", nextCall.Name, err)
		}
		return result, nil
	})
	for index := len(r.middleware) - 1; index >= 0; index-- {
		middleware := r.middleware[index]
		middlewareIndex := index
		following := next
		next = ExecuteNext(func(nextCtx context.Context, nextCall Call) (Result, error) {
			result, err := middleware(nextCtx, cloneCall(nextCall), following)
			if err != nil {
				return Result{}, fmt.Errorf("tool: execute middleware %d: %w", middlewareIndex, err)
			}
			return result, nil
		})
	}

	result, err := next(ctx, cloneCall(call))
	if err != nil {
		return Result{}, err
	}
	endTime := time.Now().UnixMilli()
	result.Metadata = ResultMetadata{
		CallID:      call.ID,
		ToolName:    call.Name,
		StartTimeMS: startTime,
		EndTimeMS:   endTime,
		DurationMS:  endTime - startTime,
		Success:     true,
	}
	return result, nil
}

func cloneCall(value Call) Call {
	value.Arguments = cloneCallArguments(value)
	return value
}

func cloneCallArguments(value Call) []byte {
	arguments := make([]byte, len(value.Arguments))
	copy(arguments, value.Arguments)
	return arguments
}

var _ Service = (*ToolRuntime)(nil)
