package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/google/uuid"
)

// requestTimeout caps the time any single REST handler will spend talking to
// the broker (publish, request/reply, etc.).
const requestTimeout = 10 * time.Second

// restAPI bundles the dependencies the HTTP handlers need: the reactive-commons
// application (gateway + event bus) and a logger. Mirrors the Java
// SampleRestController, which has both `directAsyncGateway` and
// `domainEventBus` injected.
type restAPI struct {
	app *rabbit.Application
	log *slog.Logger
}

func newRestAPI(app *rabbit.Application, log *slog.Logger) *restAPI {
	return &restAPI{app: app, log: log}
}

// register wires every endpoint onto mux using Go 1.22 method+path patterns.
func (a *restAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("DELETE /api/teams", a.resetData)
	mux.HandleFunc("GET /api/teams", a.getTeams)
	mux.HandleFunc("GET /api/teams/{team}", a.getTeamMembers)
	mux.HandleFunc("POST /api/teams/{team}/members", a.addMember)
	mux.HandleFunc("DELETE /api/teams/{team}/members/{member}", a.removeMember)
	mux.HandleFunc("GET /api/animals/{event}", a.wildCardEvent)
}

// resetData mirrors `DELETE /api/teams` on the Java sample. The Java code
// calls `domainEventBus.emit`, but the receivers (Go and Java) subscribe via
// `listenNotificationEvent`. We use EmitNotification here so the round-trip
// actually works against the receiver-responder example.
func (a *restAPI) resetData(w http.ResponseWriter, r *http.Request) {
	notification := async.Notification[any]{
		Name:    DataReset,
		EventID: uuid.NewString(),
		Data:    "",
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := a.app.EventBus().EmitNotification(ctx, notification); err != nil {
		a.writeError(w, r, "emit data-reset notification", err)
		return
	}
	a.log.Info("emitted data-reset notification", "eventId", notification.EventID)
	a.writeJSON(w, http.StatusOK, notification)
}

// getTeams mirrors `GET /api/teams` on the Java sample: a request/reply query
// against the receiver's `get-teams` resource.
func (a *restAPI) getTeams(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	raw, err := a.app.Gateway().RequestReply(ctx, async.AsyncQuery[any]{
		Resource:  GetTeams,
		QueryData: "",
	}, TargetAppName)
	if err != nil {
		a.writeError(w, r, "query get-teams", err)
		return
	}

	var teams Teams
	if err := json.Unmarshal(raw, &teams); err != nil {
		a.writeError(w, r, "decode get-teams reply", err)
		return
	}
	a.log.Info("served get-teams", "count", len(teams))
	a.writeJSON(w, http.StatusOK, teams)
}

// getTeamMembers mirrors `GET /api/teams/{team}` on the Java sample: a query
// whose `queryData` is the team name.
func (a *restAPI) getTeamMembers(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	raw, err := a.app.Gateway().RequestReply(ctx, async.AsyncQuery[any]{
		Resource:  GetTeamMembers,
		QueryData: team,
	}, TargetAppName)
	if err != nil {
		a.writeError(w, r, "query get-team-members", err)
		return
	}

	var members Members
	if err := json.Unmarshal(raw, &members); err != nil {
		a.writeError(w, r, "decode get-team-members reply", err)
		return
	}
	a.log.Info("served get-team-members", "team", team, "count", len(members))
	a.writeJSON(w, http.StatusOK, members)
}

// addMember mirrors `POST /api/teams/{team}/members` on the Java sample: a
// fire-and-forget command (`add-member`) carrying the team and the member.
func (a *restAPI) addMember(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")

	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		a.writeError(w, r, "read request body", err)
		return
	}
	var member Member
	if err := json.Unmarshal(body, &member); err != nil {
		a.writeError(w, r, "decode member", err)
		return
	}

	cmd := async.Command[any]{
		Name:      AddMember,
		CommandID: uuid.NewString(),
		Data: AddMemberCommand{
			TeamName: team,
			Member:   member,
		},
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := a.app.Gateway().SendCommand(ctx, cmd, TargetAppName); err != nil {
		a.writeError(w, r, "send add-member command", err)
		return
	}
	a.log.Info("sent add-member command", "commandId", cmd.CommandID, "team", team, "member", member.Username)
	a.writeJSON(w, http.StatusOK, cmd)
}

// removeMember mirrors `DELETE /api/teams/{team}/members/{member}` on the Java
// sample: emits a `member-removed` domain event.
func (a *restAPI) removeMember(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	member := r.PathValue("member")

	event := async.DomainEvent[any]{
		Name:    MemberRemoved,
		EventID: uuid.NewString(),
		Data: RemovedMemberEvent{
			TeamName: team,
			Username: member,
		},
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := a.app.EventBus().Emit(ctx, event); err != nil {
		a.writeError(w, r, "emit member-removed event", err)
		return
	}
	a.log.Info("emitted member-removed event", "eventId", event.EventID, "team", team, "member", member)
	a.writeJSON(w, http.StatusOK, event)
}

// wildCardEvent mirrors `GET /api/animals/{event}` on the Java sample. We
// prepend `animals.` to the path variable so the resulting routing key
// (e.g. `animals.dogs`) matches the receiver's `animals.#` binding.
func (a *restAPI) wildCardEvent(w http.ResponseWriter, r *http.Request) {
	suffix := r.PathValue("event")

	event := async.DomainEvent[any]{
		Name:    AnimalsPrefix + suffix,
		EventID: uuid.NewString(),
		Data: AnimalEvent{
			Type: "dog",
			Name: uuid.NewString(),
		},
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := a.app.EventBus().Emit(ctx, event); err != nil {
		a.writeError(w, r, "emit animal event", err)
		return
	}
	a.log.Info("emitted animal event", "eventName", event.Name, "eventId", event.EventID)
	a.writeJSON(w, http.StatusOK, event)
}

func (a *restAPI) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		a.log.Error("write response body", "error", err)
	}
}

// writeError logs the operation that failed and emits a JSON error envelope.
// Status code is 504 on context-deadline / query timeout errors, 500 otherwise.
func (a *restAPI) writeError(w http.ResponseWriter, r *http.Request, op string, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, async.ErrQueryTimeout) {
		status = http.StatusGatewayTimeout
	}
	a.log.Error("request failed", "op", op, "method", r.Method, "path", r.URL.Path, "error", err)
	a.writeJSON(w, status, map[string]string{"error": err.Error(), "op": op})
}
