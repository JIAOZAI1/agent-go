package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

var (
	// ErrInvalidContext indicates that Execute received a nil context.
	ErrInvalidContext = errors.New("tool: invalid context")
	// ErrBuilderBuilt indicates that a Builder has already built a Runtime.
	ErrBuilderBuilt = errors.New("tool: builder already built")
	// ErrNilTool indicates that a nil Tool was registered.
	ErrNilTool = errors.New("tool: nil tool")
	// ErrEmptyToolName indicates that a Tool or Call has no name.
	ErrEmptyToolName = errors.New("tool: empty tool name")
	// ErrInvalidSchema indicates that a Tool schema is not a JSON object.
	ErrInvalidSchema = errors.New("tool: invalid parameter schema")
	// ErrDuplicateTool indicates that a tool name is already registered.
	ErrDuplicateTool = errors.New("tool: duplicate tool")
	// ErrNilExecuteMiddleware indicates that a nil Middleware was registered.
	ErrNilExecuteMiddleware = errors.New("tool: nil execute middleware")
	// ErrInvalidArguments indicates that invocation arguments are not a JSON object.
	ErrInvalidArguments = errors.New("tool: invalid arguments")
	// ErrToolNotFound indicates that a Call names no registered Tool.
	ErrToolNotFound = errors.New("tool: tool not found")
	// ErrInvalidResultMetadata indicates invalid manually built metadata.
	ErrInvalidResultMetadata = errors.New("tool: invalid result metadata")
)

type registeredTool struct {
	spec Spec
	tool Tool
}

// Builder registers tools and middleware during application setup. It is
// intended for single-goroutine use and freezes after a successful Build.
type Builder struct {
	tools      map[string]registeredTool
	order      []string
	middleware []ExecuteMiddleware
	built      bool
}

// NewBuilder creates an empty Builder. An empty tool system is valid.
func NewBuilder() *Builder {
	return &Builder{tools: make(map[string]registeredTool)}
}

// AddTool registers a Tool by its unique Spec name.
func (b *Builder) AddTool(value Tool) error {
	if b == nil || b.built {
		return ErrBuilderBuilt
	}
	if isNil(value) {
		return ErrNilTool
	}
	spec := cloneSpec(value.Spec())
	if spec.Name == "" {
		return ErrEmptyToolName
	}
	if !isJSONObject(spec.Parameters) {
		return fmt.Errorf("%w: %s", ErrInvalidSchema, spec.Name)
	}
	if b.tools == nil {
		b.tools = make(map[string]registeredTool)
	}
	if _, exists := b.tools[spec.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, spec.Name)
	}
	b.tools[spec.Name] = registeredTool{spec: spec, tool: value}
	b.order = append(b.order, spec.Name)
	return nil
}

// AddMiddleware appends an execution Middleware in chain order.
func (b *Builder) AddMiddleware(value ExecuteMiddleware) error {
	if b == nil || b.built {
		return ErrBuilderBuilt
	}
	if value == nil {
		return ErrNilExecuteMiddleware
	}
	b.middleware = append(b.middleware, value)
	return nil
}

// Build snapshots the catalog and returns an immutable ToolRuntime.
func (b *Builder) Build() (*ToolRuntime, error) {
	if b == nil || b.built {
		return nil, ErrBuilderBuilt
	}
	tools := make(map[string]registeredTool, len(b.tools))
	specs := make([]Spec, 0, len(b.order))
	for _, name := range b.order {
		registered := b.tools[name]
		registered.spec = cloneSpec(registered.spec)
		tools[name] = registered
		specs = append(specs, cloneSpec(registered.spec))
	}
	b.built = true
	return &ToolRuntime{
		tools:      tools,
		specs:      specs,
		middleware: append([]ExecuteMiddleware(nil), b.middleware...),
	}, nil
}

// ResultMetadataBuilder builds validated ResultMetadata values.
type ResultMetadataBuilder struct{ value ResultMetadata }

// NewResultMetadataBuilder creates an empty metadata builder.
func NewResultMetadataBuilder() *ResultMetadataBuilder { return &ResultMetadataBuilder{} }

// CallID sets the model call ID.
func (b *ResultMetadataBuilder) CallID(value string) *ResultMetadataBuilder {
	b.value.CallID = value
	return b
}

// ToolName sets the executed tool name.
func (b *ResultMetadataBuilder) ToolName(value string) *ResultMetadataBuilder {
	b.value.ToolName = value
	return b
}

// StartTimeMS sets the start timestamp.
func (b *ResultMetadataBuilder) StartTimeMS(value int64) *ResultMetadataBuilder {
	b.value.StartTimeMS = value
	return b
}

// EndTimeMS sets the end timestamp.
func (b *ResultMetadataBuilder) EndTimeMS(value int64) *ResultMetadataBuilder {
	b.value.EndTimeMS = value
	return b
}

// DurationMS sets the elapsed duration.
func (b *ResultMetadataBuilder) DurationMS(value int64) *ResultMetadataBuilder {
	b.value.DurationMS = value
	return b
}

// Success sets whether execution produced a result.
func (b *ResultMetadataBuilder) Success(value bool) *ResultMetadataBuilder {
	b.value.Success = value
	return b
}

// Build validates and returns metadata.
func (b *ResultMetadataBuilder) Build() (ResultMetadata, error) {
	if b == nil || b.value.StartTimeMS < 0 || b.value.EndTimeMS < 0 || b.value.DurationMS < 0 || b.value.EndTimeMS < b.value.StartTimeMS {
		return ResultMetadata{}, ErrInvalidResultMetadata
	}
	return b.value, nil
}

func cloneSpec(value Spec) Spec {
	value.Parameters = bytes.Clone(value.Parameters)
	return value
}

func isJSONObject(value json.RawMessage) bool {
	if len(value) == 0 || !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
