package app

import (
	"strings"

	"github.com/HeimaoLST/Nagi/internal/store"
)

func saveContacts(s *contactStore, contacts []*contact, selected string) error {
	return saveContactsWithPreferences(s, contacts, selected, s.State.Appearance, s.State.ThemeIDs, s.State.Layout)
}

func saveContactsWithPreferences(s *contactStore, contacts []*contact, selected, appearance string, themeIDs *appearanceThemeIDs, layout *storedLayout) error {
	candidate := storedState{
		Version:             contactStoreVersion,
		Selected:            selected,
		Contacts:            make([]storedContact, 0, len(contacts)),
		IssueToolOperations: store.CloneIssueToolLedger(s.IssueToolLedger),
		IssueToolScopes:     s.IssueToolScopesList(),
		Appearance:          appearance,
	}
	if layout != nil {
		value := *layout
		candidate.Layout = &value
	}
	if themeIDs != nil {
		value := *themeIDs
		candidate.ThemeIDs = &value
	}
	for _, c := range contacts {
		storedCt := storedContact{
			ID:           c.ID,
			Name:         c.Name,
			Role:         c.Role,
			Purpose:      c.Purpose,
			Instructions: c.Instructions,
			Workspace:    c.Workspace,
			BuiltinTools: cloneBuiltinTools(c.BuiltinTools),
			SessionID:    c.SessionID,
			Draft:        c.Draft,
			TaskContext:  c.Context.Snapshot(),
			Archived:     c.Archived,
			ArchivedAt:   c.ArchivedAt,

			PiBinding:          clonePiConversationBinding(c.PiBinding),
			PreviousPiBindings: c.piPreviousBindings(),
			PendingSend:        clonePendingSend(c.pendingSend),
			SelectedIssueID:    c.SelectedActivityID,
			UpdatedAt:          nowMillis(),
		}
		candidate.Contacts = append(candidate.Contacts, storedCt)
		for _, activity := range c.Activities {
			stored := storedActivity{
				ID: activity.ID, ContactID: activity.ContactID, Goal: activity.Goal,
				Status: activity.Status, Archived: activity.Archived,
				Evidence: activity.Evidence, CreatedAt: activity.CreatedAt, UpdatedAt: activity.UpdatedAt,
				PiBinding:     clonePiActivityBinding(activity.PiBinding),
				Handoff:       cloneActivityHandoff(activity.Handoff),
				WorkSessionID: activity.WorkSessionID, RequirementsRevision: activity.RequirementsRevision,
				TaskContext: activity.Context.Snapshot(), Supplements: cloneActivitySupplementLedger(activity.Supplements),
				Questions: store.CloneIssueQuestions(activity.Questions),
			}
			for _, requirement := range activity.Requirements {
				if requirement == nil || strings.TrimSpace(requirement.Text) == "" {
					continue
				}
				stored.RequirementItems = append(stored.RequirementItems, storedRequirement{
					ID: requirement.ID, Text: requirement.Text, State: requirement.State,
					Evidence: requirement.Evidence, UpdatedAt: requirement.UpdatedAt,
				})
			}
			for _, execution := range activity.Executions {
				stored.Executions = append(stored.Executions, storedActivityExecution{
					ID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
					MessageID: execution.MessageID, Prompt: execution.Prompt,
					Status: execution.Status, Detail: execution.Detail,
					AttentionSeenStatus:  execution.AttentionSeenStatus,
					RequirementsRevision: execution.RequirementsRevision,
					Context:              cloneTaskContextItems(execution.Context),
					SupplementBatchID:    execution.SupplementBatchID,
					ToolOperationID:      execution.ToolOperationID,
					ToolScopeRef:         execution.ToolScopeRef,
					PiIntent:             execution.PiIntent, PiRun: clonePiActivityRun(execution.PiRun),
					ReturnedAt:    execution.ReturnedAt,
					WaitRequested: execution.WaitRequested,
				})
			}
			candidate.Issues = append(candidate.Issues, stored)
		}
	}
	if len(contacts) > 0 {
		found := false
		for _, ct := range candidate.Contacts {
			if ct.ID == selected {
				found = true
				break
			}
		}
		if !found {
			candidate.Selected = contacts[0].ID
		}
	}
	return s.CommitState(candidate)
}
