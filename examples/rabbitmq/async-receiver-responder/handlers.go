package main

import (
	"github.com/bancolombia/reactive-commons-go/rabbit"
)

// RegisterHandlers wires the UseCase methods onto the application's handler
// registry. Mirrors the @Bean handlerRegistrySubs in the Java HandlersConfig.
func RegisterHandlers(app *rabbit.Application, uc *UseCase) error {
	r := app.Registry()
	if err := r.ServeQuery(GetTeams, uc.GetTeamsHandler); err != nil {
		return err
	}
	if err := r.ServeQuery(GetTeamMembers, uc.GetTeamHandler); err != nil {
		return err
	}
	if err := r.ListenCommand(AddMember, uc.AddMemberHandler); err != nil {
		return err
	}
	if err := r.ListenEvent(MemberRemoved, uc.RemoveMemberHandler); err != nil {
		return err
	}
	if err := r.ListenEvent(AnimalsMany, uc.AnimalEventHandler); err != nil {
		return err
	}
	return r.ListenNotification(DataReset, uc.ResetHandler)
}
