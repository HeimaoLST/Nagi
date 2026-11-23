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
			SelectedIssueID:    c.SelectedIssueID,
			UpdatedAt:          nowMillis(),
		}
		candidate.Contacts = append(candidate.Contacts, storedCt)
		for _, issue := range c.Issues {
			stored := storedIssue{
				ID: issue.ID, ContactID: issue.ContactID, Goal: issue.Goal,
				Status: issue.Status, Archived: issue.Archived,
				Evidence: issue.Evidence, CreatedAt: issue.CreatedAt, UpdatedAt: issue.UpdatedAt,
				PiBinding:     clonePiActivityBinding(issue.PiBinding),
				Handoff:       cloneIssueHandoff(issue.Handoff),
				WorkSessionID: issue.WorkSessionID, RequirementsRevision: issue.RequirementsRevision,
				TaskContext: issue.Context.Snapshot(), Supplements: cloneIssueSupplementLedger(issue.Supplements),
				Questions: store.CloneIssueQuestions(issue.Questions),
			}
			for _, requirement := range issue.Requirements {
				if requirement == nil || strings.TrimSpace(requirement.Text) == "" {
					continue
				}
				stored.RequirementItems = append(stored.RequirementItems, storedRequirement{
					ID: requirement.ID, Text: requirement.Text, State: requirement.State,
					Evidence: requirement.Evidence, UpdatedAt: requirement.UpdatedAt,
				})
			}
			for _, execution := range issue.Executions {
				stored.Executions = append(stored.Executions, storedIssueExecution{
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
