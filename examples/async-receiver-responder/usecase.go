package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// UseCase mirrors the Java sample's UseCase class. It owns an in-memory Teams
// map and exposes typed handler methods that the registry wires up. All state
// access goes through mu so the various consumer goroutines can call
// concurrently without races.
type UseCase struct {
	log *slog.Logger
	mu  sync.RWMutex
	tms Teams
}

// NewUseCase returns a UseCase with an empty Teams map.
func NewUseCase(log *slog.Logger) *UseCase {
	return &UseCase{log: log, tms: make(Teams)}
}

// GetTeamsHandler serves the `get-teams` query. The query payload is ignored,
// matching the Java implementation which accepts a `String ignored` arg.
func (u *UseCase) GetTeamsHandler(_ context.Context, _ async.AsyncQuery[any], _ async.From) (any, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	out := make(Teams, len(u.tms))
	for k, v := range u.tms {
		out[k] = v
	}
	u.log.Info("serving get-teams query", "count", len(out))
	return out, nil
}

// GetTeamHandler serves the `get-team-members` query. The query payload is the
// team name as a JSON string.
func (u *UseCase) GetTeamHandler(_ context.Context, q async.AsyncQuery[any], _ async.From) (any, error) {
	raw, _ := q.QueryData.(json.RawMessage)
	var teamName string
	if err := json.Unmarshal(raw, &teamName); err != nil {
		return nil, fmt.Errorf("decode team name: %w", err)
	}

	u.mu.RLock()
	defer u.mu.RUnlock()
	if t, ok := u.tms[teamName]; ok {
		u.log.Info("serving get-team-members query", "team", teamName, "size", len(t.Members))
		return t.Members, nil
	}
	u.log.Info("serving get-team-members query for unknown team", "team", teamName)
	return Members{}, nil
}

// AddMemberHandler handles the `add-member` command and adds the member to the
// named team, creating the team on demand.
func (u *UseCase) AddMemberHandler(_ context.Context, cmd async.Command[any]) error {
	raw, _ := cmd.Data.(json.RawMessage)
	var data AddMemberCommand
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("decode add-member command: %w", err)
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	t, ok := u.tms[data.TeamName]
	if !ok {
		t = Team{Name: data.TeamName, Members: Members{}}
	}
	t.Members = append(t.Members, data.Member)
	u.tms[data.TeamName] = t
	u.log.Info("added member", "commandId", cmd.CommandID, "team", data.TeamName, "member", data.Member.Username)
	return nil
}

// RemoveMemberHandler handles the `member-removed` event by stripping the
// matching username from the team's member list.
func (u *UseCase) RemoveMemberHandler(_ context.Context, ev async.DomainEvent[any]) error {
	raw, _ := ev.Data.(json.RawMessage)
	var data RemovedMemberEvent
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("decode member-removed event: %w", err)
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	t, ok := u.tms[data.TeamName]
	if !ok {
		u.log.Info("ignoring member-removed for unknown team", "team", data.TeamName)
		return nil
	}
	filtered := t.Members[:0]
	for _, m := range t.Members {
		if m.Username != data.Username {
			filtered = append(filtered, m)
		}
	}
	t.Members = filtered
	u.tms[data.TeamName] = t
	u.log.Info("removed member", "eventId", ev.EventID, "team", data.TeamName, "username", data.Username)
	return nil
}

// AnimalEventHandler handles every event matching `animals.#`. The Java sample
// returns Mono.error to nack the message; we mirror that by returning an error.
func (u *UseCase) AnimalEventHandler(_ context.Context, ev async.DomainEvent[any]) error {
	raw, _ := ev.Data.(json.RawMessage)
	var data AnimalEvent
	if err := json.Unmarshal(raw, &data); err != nil {
		u.log.Error("decode animal event", "error", err, "eventName", ev.Name)
		return err
	}
	u.log.Info("received animal event", "eventName", ev.Name, "eventId", ev.EventID, "name", data.Name, "type", data.Type)
	return fmt.Errorf("not implemented")
}

// ResetHandler handles the `data-reset` notification by clearing all teams.
func (u *UseCase) ResetHandler(_ context.Context, n async.Notification[any]) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.tms = make(Teams)
	u.log.Info("teams reset", "eventId", n.EventID)
	return nil
}
