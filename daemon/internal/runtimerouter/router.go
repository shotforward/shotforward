package runtimerouter

import (
	"context"
	"errors"
	"strings"
	"sync"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

type Unavailable struct{ SnapshotValue agentapp.RuntimeSnapshot }

type cloudTurnFailures interface {
	BeginAgentTurn(string) func() string
	EndAgentTurn(string) string
}

func (u Unavailable) Snapshot(context.Context) (agentapp.RuntimeSnapshot, error) {
	return u.SnapshotValue, errors.New(u.SnapshotValue.FailureSummary)
}
func (u Unavailable) Models(context.Context) ([]agentapp.Model, error) {
	return nil, errors.New(u.SnapshotValue.FailureSummary)
}
func (u Unavailable) StartLogin(context.Context, agentapp.LoginMode) (agentapp.LoginChallenge, error) {
	return agentapp.LoginChallenge{}, errors.New(u.SnapshotValue.FailureSummary)
}
func (u Unavailable) StartThread(context.Context, agentapp.ThreadRequest) (agentapp.Thread, error) {
	return agentapp.Thread{}, errors.New(u.SnapshotValue.FailureSummary)
}
func (u Unavailable) ResumeThread(context.Context, string, agentapp.ThreadRequest) (agentapp.Thread, error) {
	return agentapp.Thread{}, errors.New(u.SnapshotValue.FailureSummary)
}
func (u Unavailable) ReadThread(context.Context, string) (agentapp.Thread, error) {
	return agentapp.Thread{}, errors.New(u.SnapshotValue.FailureSummary)
}
func (u Unavailable) ListConversation(context.Context, string, string, int) (agentapp.RuntimeConversationPage, error) {
	return agentapp.RuntimeConversationPage{}, errors.New(u.SnapshotValue.FailureSummary)
}
func (u Unavailable) StartTurn(context.Context, agentapp.TurnRequest) (agentapp.RuntimeTurn, error) {
	return agentapp.RuntimeTurn{}, errors.New(u.SnapshotValue.FailureSummary)
}
func (u Unavailable) SteerTurn(context.Context, string, string, string) error {
	return errors.New(u.SnapshotValue.FailureSummary)
}
func (u Unavailable) InterruptTurn(context.Context, string, string) error {
	return errors.New(u.SnapshotValue.FailureSummary)
}
func (u Unavailable) Subscribe(context.Context, string) (<-chan agentapp.RuntimeEvent, error) {
	return nil, errors.New(u.SnapshotValue.FailureSummary)
}

const separator = "|"

type SelectionStore interface {
	ActiveRuntime(context.Context) (agentapp.RuntimeKind, error)
	SetActiveRuntime(context.Context, agentapp.RuntimeKind) error
	HasActiveTurns(context.Context) (bool, error)
	RuntimeWithActiveTurns(context.Context) (agentapp.RuntimeKind, bool, error)
}

// ModelSource is the authoritative product catalog for a routed Runtime. The
// Runtime still owns execution metadata; this source decides which of those
// locally understood models the product currently exposes.
type ModelSource interface {
	Models(context.Context) ([]agentapp.Model, error)
}

type refreshingModelSource interface {
	RefreshModels(context.Context) ([]agentapp.Model, error)
}

type Router struct {
	User, ShotForward agentapp.Runtime
	shotForwardModels ModelSource
	selection         SelectionStore
	selectionMutex    sync.Mutex
}

func New(user, shotForward agentapp.Runtime, selections ...SelectionStore) (*Router, error) {
	return newRouter(user, shotForward, nil, selections...)
}

func NewWithShotForwardModels(user, shotForward agentapp.Runtime, models ModelSource, selections ...SelectionStore) (*Router, error) {
	if models == nil {
		return nil, errors.New("ShotForward Agent model source is required")
	}
	return newRouter(user, shotForward, models, selections...)
}

func newRouter(user, shotForward agentapp.Runtime, models ModelSource, selections ...SelectionStore) (*Router, error) {
	if user == nil || shotForward == nil {
		return nil, errors.New("both Agent runtimes are required")
	}
	if len(selections) > 1 || (len(selections) == 1 && selections[0] == nil) {
		return nil, errors.New("at most one Agent Runtime selection store is supported")
	}
	result := &Router{User: user, ShotForward: shotForward, shotForwardModels: models}
	if len(selections) == 1 {
		result.selection = selections[0]
	}
	return result, nil
}

func (r *Router) Snapshot(ctx context.Context) (agentapp.RuntimeSnapshot, error) {
	kind, err := r.ActiveRuntime(ctx)
	if err != nil {
		return agentapp.RuntimeSnapshot{}, err
	}
	return r.SnapshotFor(ctx, kind)
}

func (r *Router) SnapshotFor(ctx context.Context, kind agentapp.RuntimeKind) (agentapp.RuntimeSnapshot, error) {
	return r.runtime(normalizeKind(kind)).Snapshot(ctx)
}

func (r *Router) Models(ctx context.Context) ([]agentapp.Model, error) {
	if r.selection == nil {
		var result []agentapp.Model
		if values, err := r.modelsFor(ctx, agentapp.RuntimeUserCodex); err == nil {
			result = append(result, values...)
		}
		if values, err := r.modelsFor(ctx, agentapp.RuntimeShotForward); err == nil {
			result = append(result, values...)
		}
		return result, nil
	}
	kind, err := r.ActiveRuntime(ctx)
	if err != nil {
		return nil, err
	}
	return r.modelsFor(ctx, kind)
}

func (r *Router) ModelsFor(ctx context.Context, kind agentapp.RuntimeKind) ([]agentapp.Model, error) {
	if !validKind(kind) {
		return nil, agentapp.ErrInvalidRuntimeSelection
	}
	return r.modelsFor(ctx, kind)
}

func (r *Router) RefreshModelsFor(ctx context.Context, kind agentapp.RuntimeKind) ([]agentapp.Model, error) {
	if !validKind(kind) {
		return nil, agentapp.ErrInvalidRuntimeSelection
	}
	return r.modelsForWithRefresh(ctx, kind, true)
}

func (r *Router) ActiveRuntime(ctx context.Context) (agentapp.RuntimeKind, error) {
	if r.selection == nil {
		return agentapp.RuntimeUserCodex, nil
	}
	kind, err := r.selection.ActiveRuntime(ctx)
	if err != nil {
		return "", err
	}
	if validKind(kind) {
		return kind, nil
	}

	// Serialize first-run resolution. ShotForward wins when both endpoints can
	// enumerate models; otherwise a working local Codex remains immediately usable.
	r.selectionMutex.Lock()
	defer r.selectionMutex.Unlock()
	kind, err = r.selection.ActiveRuntime(ctx)
	if err != nil {
		return "", err
	}
	if validKind(kind) {
		return kind, nil
	}
	kind = agentapp.RuntimeUserCodex
	if activeKind, found, activeErr := r.selection.RuntimeWithActiveTurns(ctx); activeErr != nil {
		return "", activeErr
	} else if found && validKind(activeKind) {
		kind = activeKind
	} else if models, cloudErr := r.modelsFor(ctx, agentapp.RuntimeShotForward); cloudErr == nil && len(models) > 0 {
		kind = agentapp.RuntimeShotForward
	}
	if err := r.selection.SetActiveRuntime(ctx, kind); err != nil {
		return "", err
	}
	return kind, nil
}

func (r *Router) SelectRuntime(ctx context.Context, kind agentapp.RuntimeKind) error {
	if !validKind(kind) || r.selection == nil {
		return agentapp.ErrInvalidRuntimeSelection
	}
	r.selectionMutex.Lock()
	defer r.selectionMutex.Unlock()
	current, err := r.selection.ActiveRuntime(ctx)
	if err != nil {
		return err
	}
	if current == kind {
		return nil
	}
	active, err := r.selection.HasActiveTurns(ctx)
	if err != nil {
		return err
	}
	if active {
		return agentapp.ErrRuntimeSwitchBlocked
	}
	return r.selection.SetActiveRuntime(ctx, kind)
}

func (r *Router) StartLogin(ctx context.Context, mode agentapp.LoginMode) (agentapp.LoginChallenge, error) {
	return r.User.StartLogin(ctx, mode)
}

func (r *Router) NetworkSettings(ctx context.Context) (agentapp.RuntimeNetworkSettings, error) {
	configurator, ok := r.User.(agentapp.RuntimeNetworkConfigurator)
	if !ok {
		return agentapp.RuntimeNetworkSettings{}, errors.New("Codex Runtime network settings are unavailable")
	}
	return configurator.NetworkSettings(ctx)
}

func (r *Router) ConfigureNetwork(ctx context.Context, settings agentapp.RuntimeNetworkSettings) error {
	configurator, ok := r.User.(agentapp.RuntimeNetworkConfigurator)
	if !ok {
		return errors.New("Codex Runtime network settings are unavailable")
	}
	return configurator.ConfigureNetwork(ctx, settings)
}

func (r *Router) StartThread(ctx context.Context, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	if r.selection == nil {
		kind := normalizeKind(request.RuntimeKind)
		request.RuntimeKind = kind
		thread, err := r.runtime(kind).StartThread(ctx, request)
		if err != nil {
			return thread, err
		}
		return prefixThread(kind, thread), nil
	}
	kind, err := r.ActiveRuntime(ctx)
	if err != nil {
		return agentapp.Thread{}, err
	}
	if request.RuntimeKind != "" && request.RuntimeKind != kind {
		return agentapp.Thread{}, agentapp.ErrInvalidRuntimeSelection
	}
	target := r.runtime(kind)
	request.RuntimeKind = kind
	thread, err := target.StartThread(ctx, request)
	if err != nil {
		return thread, err
	}
	return prefixThread(kind, thread), nil
}

func (r *Router) ResumeThread(ctx context.Context, threadID string, request agentapp.ThreadRequest) (agentapp.Thread, error) {
	kind, raw := parse(threadID)
	if err := r.ensureActive(ctx, kind); err != nil {
		return agentapp.Thread{}, err
	}
	if normalizeKind(request.RuntimeKind) != kind {
		return agentapp.Thread{}, errors.New("Agent Session runtime does not match persisted Thread")
	}
	thread, err := r.runtime(kind).ResumeThread(ctx, raw, request)
	if err != nil {
		return thread, err
	}
	return prefixThread(kind, thread), nil
}

func (r *Router) ReadThread(ctx context.Context, threadID string) (agentapp.Thread, error) {
	kind, raw := parse(threadID)
	thread, err := r.runtime(kind).ReadThread(ctx, raw)
	if err != nil {
		return thread, err
	}
	return prefixThread(kind, thread), nil
}
func (r *Router) ListConversation(ctx context.Context, threadID, cursor string, limit int) (agentapp.RuntimeConversationPage, error) {
	kind, raw := parse(threadID)
	return r.runtime(kind).ListConversation(ctx, raw, cursor, limit)
}
func (r *Router) StartTurn(ctx context.Context, request agentapp.TurnRequest) (agentapp.RuntimeTurn, error) {
	kind, raw := parse(request.CodexThreadID)
	if err := r.ensureActive(ctx, kind); err != nil {
		return agentapp.RuntimeTurn{}, err
	}
	request.CodexThreadID = raw
	turn, err := r.runtime(kind).StartTurn(ctx, request)
	if err != nil && kind == agentapp.RuntimeShotForward {
		if failures, ok := r.shotForwardModels.(cloudTurnFailures); ok {
			if code := failures.EndAgentTurn(raw); code != "" {
				err = generation.NewGeneratorFailure(code, errors.New("ShotForward account cannot accept this request"))
			}
		}
	}
	return turn, err
}
func (r *Router) SteerTurn(ctx context.Context, threadID, turnID, text string) error {
	kind, raw := parse(threadID)
	if err := r.ensureActive(ctx, kind); err != nil {
		return err
	}
	return r.runtime(kind).SteerTurn(ctx, raw, turnID, text)
}
func (r *Router) SteerTurnWithImages(ctx context.Context, threadID, turnID, text string, imagePaths []string) error {
	kind, raw := parse(threadID)
	if err := r.ensureActive(ctx, kind); err != nil {
		return err
	}
	runtime, ok := r.runtime(kind).(agentapp.ImageSteeringRuntime)
	if !ok {
		return errors.New("active Agent Runtime does not support image input")
	}
	return runtime.SteerTurnWithImages(ctx, raw, turnID, text, imagePaths)
}
func (r *Router) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	kind, raw := parse(threadID)
	return r.runtime(kind).InterruptTurn(ctx, raw, turnID)
}
func (r *Router) CompactThread(ctx context.Context, threadID string) error {
	kind, raw := parse(threadID)
	if err := r.ensureActive(ctx, kind); err != nil {
		return err
	}
	runtime, ok := r.runtime(kind).(agentapp.ContextCompactingRuntime)
	if !ok {
		return agentapp.ErrContextCompactionUnavailable
	}
	return runtime.CompactThread(ctx, raw)
}
func (r *Router) ContextCompactionInProgress(threadID string) bool {
	kind, raw := parse(threadID)
	runtime, ok := r.runtime(kind).(agentapp.ContextCompactionStatusRuntime)
	return ok && runtime.ContextCompactionInProgress(raw)
}
func (r *Router) Subscribe(ctx context.Context, threadID string) (<-chan agentapp.RuntimeEvent, error) {
	kind, raw := parse(threadID)
	if err := r.ensureActive(ctx, kind); err != nil {
		return nil, err
	}
	source, err := r.runtime(kind).Subscribe(ctx, raw)
	if err != nil {
		return nil, err
	}
	output := make(chan agentapp.RuntimeEvent, 32)
	finish := func() string { return "" }
	if kind == agentapp.RuntimeShotForward {
		if failures, ok := r.shotForwardModels.(cloudTurnFailures); ok {
			finish = failures.BeginAgentTurn(raw)
		}
	}
	go func() {
		defer close(output)
		defer finish()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-source:
				if !ok {
					return
				}
				if event.Type == agentapp.RuntimeTurnCompleted {
					if code := finish(); code != "" && event.TurnStatus == "failed" {
						event.FailureCode = code
						event.FailureSummary = "ShotForward account cannot accept this request"
					}
				}
				event.CodexThreadID = prefix(kind, event.CodexThreadID)
				select {
				case output <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return output, nil
}

func (r *Router) runtime(kind agentapp.RuntimeKind) agentapp.Runtime {
	if kind == agentapp.RuntimeShotForward {
		return r.ShotForward
	}
	return r.User
}
func (r *Router) modelsFor(ctx context.Context, kind agentapp.RuntimeKind) ([]agentapp.Model, error) {
	return r.modelsForWithRefresh(ctx, kind, false)
}

func (r *Router) modelsForWithRefresh(ctx context.Context, kind agentapp.RuntimeKind, refresh bool) ([]agentapp.Model, error) {
	values, err := r.runtime(kind).Models(ctx)
	if err != nil {
		return nil, err
	}
	if kind == agentapp.RuntimeShotForward && r.shotForwardModels != nil {
		var authoritative []agentapp.Model
		var catalogErr error
		if source, ok := r.shotForwardModels.(refreshingModelSource); refresh && ok {
			authoritative, catalogErr = source.RefreshModels(ctx)
		} else {
			authoritative, catalogErr = r.shotForwardModels.Models(ctx)
		}
		if catalogErr != nil {
			return nil, catalogErr
		}
		available := make(map[string]agentapp.Model, len(values))
		for _, value := range values {
			available[value.ID] = value
		}
		filtered := make([]agentapp.Model, 0, len(authoritative))
		seen := make(map[string]struct{}, len(authoritative))
		for _, published := range authoritative {
			local, ok := available[published.ID]
			if !ok {
				continue
			}
			if _, duplicate := seen[published.ID]; duplicate {
				continue
			}
			seen[published.ID] = struct{}{}
			if published.DisplayName != "" {
				local.DisplayName = published.DisplayName
			}
			filtered = append(filtered, local)
		}
		values = filtered
	}
	for index := range values {
		values[index].RuntimeKind = kind
	}
	return values, nil
}
func validKind(kind agentapp.RuntimeKind) bool {
	return kind == agentapp.RuntimeUserCodex || kind == agentapp.RuntimeShotForward
}
func (r *Router) ensureActive(ctx context.Context, kind agentapp.RuntimeKind) error {
	if r.selection == nil {
		return nil
	}
	active, err := r.ActiveRuntime(ctx)
	if err != nil {
		return err
	}
	if active != kind {
		return agentapp.ErrInvalidRuntimeSelection
	}
	return nil
}
func normalizeKind(kind agentapp.RuntimeKind) agentapp.RuntimeKind {
	if kind == agentapp.RuntimeShotForward {
		return kind
	}
	return agentapp.RuntimeUserCodex
}
func parse(value string) (agentapp.RuntimeKind, string) {
	if strings.HasPrefix(value, string(agentapp.RuntimeShotForward)+separator) {
		return agentapp.RuntimeShotForward, strings.TrimPrefix(value, string(agentapp.RuntimeShotForward)+separator)
	}
	if strings.HasPrefix(value, string(agentapp.RuntimeUserCodex)+separator) {
		return agentapp.RuntimeUserCodex, strings.TrimPrefix(value, string(agentapp.RuntimeUserCodex)+separator)
	}
	return agentapp.RuntimeUserCodex, value
}
func prefix(kind agentapp.RuntimeKind, value string) string {
	if value == "" {
		return ""
	}
	return string(kind) + separator + value
}
func prefixThread(kind agentapp.RuntimeKind, value agentapp.Thread) agentapp.Thread {
	value.CodexThreadID = prefix(kind, value.CodexThreadID)
	value.CodexSessionID = prefix(kind, value.CodexSessionID)
	return value
}
