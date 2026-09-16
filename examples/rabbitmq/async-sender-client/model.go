package main

// Resource and routing-key constants shared with reactive-commons-java's
// `samples/async/shared` module. Keeping these names byte-identical guarantees
// that the Go sender is wire-compatible with both the Go and Java receivers.
const (
	GetTeamMembers = "get-team-members"
	GetTeams       = "get-teams"
	AddMember      = "add-member"
	MemberRemoved  = "member-removed"
	DataReset      = "data-reset"
	// AnimalsPrefix is prepended to the {event} path variable on the
	// `/api/animals/{event}` endpoint so the resulting routing key matches
	// the receiver's `animals.#` binding.
	AnimalsPrefix = "animals."
)

// TargetAppName is the receiver service the sender publishes to. Mirrors the
// `target = "receiver"` field on the Java SampleRestController.
const TargetAppName = "receiver"

// Member is a single team member.
type Member struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

// Members is a list of team members. Mirrors the Java `Members extends ArrayList<Member>`.
type Members []Member

// Team groups a list of members under a name.
type Team struct {
	Name    string  `json:"name"`
	Members Members `json:"members"`
}

// Teams maps team name -> team. Mirrors the Java `Teams extends HashMap<String, Team>`.
type Teams map[string]Team

// AddMemberCommand is the payload for the `add-member` command.
type AddMemberCommand struct {
	TeamName string `json:"teamName"`
	Member   Member `json:"member"`
}

// AnimalEvent is the payload for the `animals.*` event family.
type AnimalEvent struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// RemovedMemberEvent is the payload for the `member-removed` event.
type RemovedMemberEvent struct {
	TeamName string `json:"teamName"`
	Username string `json:"username"`
}
