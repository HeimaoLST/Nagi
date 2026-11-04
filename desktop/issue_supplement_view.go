package main

// Supplement UI consumes the same productIssue/ledger facts as the automatic
// path. Every callback mutates only the app model; it never claims model
// adoption or requirement satisfaction.

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
)

func (a *appModel) issueSupplementSection(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, editable bool) {
	msgs := locales.In(c.Locale())
	ledger := issue.Supplements
	auto := ledger == nil || ledger.AutoContinue
	pending := 0
	batches := []*issueSupplementBatch(nil)
	if ledger != nil {
		pending = ledger.pendingCount()
		batches = ledger.Batches
	}
	target := issueSupplementTargetFor(ct, issue)
	if target.IssueID == "" {
		return
	}
	formOpen := a.issueSupplementFormID == issue.ID
	if pending == 0 && len(batches) == 0 && !formOpen {
		return
	}
	title := msgs.SupplementTitle()
	if pending > 0 {
		title = msgs.SupplementTitlePending(pending)
	}
	ui.Text(c, title).FontSize(typeAction).FontWeight(500).TextColor(t.Text)
	if auto {
		ui.Text(c, msgs.SupplementAutoOn()).FontSize(14).TextColor(t.TextMuted)
	} else {
		ui.Text(c, msgs.SupplementAutoStopped()).FontSize(14).TextColor(t.Warning)
	}
	ui.Text(c, msgs.SupplementCurrentTarget(fmt.Sprint(issue.RequirementsRevision), truncate(issue.Goal, 100))).
		FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	if ledger != nil {
		if active := ledger.activeBatch(target); active != nil {
			switch active.State {
			case supplementBatchDispatching:
				ui.Text(c, msgs.SupplementDispatching()).
					FontSize(14).LineHeight(1.5).TextColor(t.Warning)
			case supplementBatchUnknown:
				ui.Text(c, msgs.SupplementUnknown()).
					FontSize(14).LineHeight(1.5).TextColor(t.Warning)
			}
		} else if pending > 0 && auto {
			if reason := a.canStartIssue(ct, issue); reason != "" {
				ui.Text(c, locales.In(c.Locale()).WaitingReason(sendBlockText(locales.In(c.Locale()), reason))).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
			} else if len(issue.Executions) == 0 {
				ui.Text(c, msgs.SupplementNoExecution()).
					FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
			} else {
				ui.Text(c, msgs.SupplementReady()).FontSize(14).TextColor(t.TextMuted)
			}
		} else if pending > 0 {
			ui.Text(c, msgs.SupplementPendingPreserved()).
				FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		}
	}
	if ledger != nil {
		var pendingItems []*issueSupplement
		for _, item := range ledger.Items {
			if item != nil && item.State == supplementStatePending {
				pendingItems = append(pendingItems, item)
			}
		}
		if len(pendingItems) > 0 {
			ui.Text(c, msgs.SupplementSavedPending()).FontSize(typeAction).FontWeight(500).TextColor(t.TextMuted)
			for _, item := range pendingItems {
				item := item
				ui.Column(c).Padding(6).Radius(6).Background(t.Surface).Gap(2).Children(func() {
					ui.Text(c, msgs.SupplementReceived(fmt.Sprint(item.RequirementsRevision), truncate(item.Text, 180))).
						FontSize(14).LineHeight(1.5).TextColor(t.Text)
					if a.issueSupplementPreviewID == item.ID {
						ui.Row(c).Children(func() {
							secondaryButton(c, msgs.SupplementClosePreview()).OnClick(func() { a.issueSupplementPreviewID = "" })
						})
						markdownReader(c, item.Text, markdownReaderOptions{PlainText: true, CopyLabel: locales.In(c.Locale()).CopySupplementTextLabel()}).
							FontSize(14).LineHeight(1.5).TextColor(t.Text)
					} else {
						secondaryButton(c, msgs.SupplementPreviewFull()).OnClick(func() { a.issueSupplementPreviewID = item.ID })
					}
				})
			}
		}
	}
	if editable {
		if !formOpen {
			ui.Row(c).Gap(6).Children(func() {
				secondaryButton(c, msgs.SupplementAdd()).TextColor(t.Text).OnClick(func() {
					a.openIssueSupplementForm(issue.ID)
				})
			})
		} else {
			draft := a.issueSupplementDraft(issue.ID)
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, msgs.SupplementAddLabel()).FontSize(typeAction).FontWeight(500).TextColor(t.TextMuted)
				ui.Spacer(c)
				secondaryButton(c, msgs.SupplementCancel()).OnClick(func() {
					a.closeIssueSupplementForm()
				})
			})
			ui.TextArea(c, draft).Label(msgs.SupplementFormLabel()).FontSize(14).Height(56).
				Placeholder(msgs.SupplementFormPlaceholder())
			ui.Row(c).Gap(6).Children(func() {
				secondaryButton(c, locales.In(c.Locale()).IssueSupplementSave()).OnClick(func() {
					if !a.saveIssueSupplement(ct, issue.ID, *draft) {
						return
					}
					*draft = ""
					a.closeIssueSupplementForm()
				})
			})
			ui.Text(c, msgs.SupplementSaveDisclaimer()).
				FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		}
		if ledger != nil {
			if auto {
				secondaryButton(c, msgs.SupplementStopAuto()).TextColor(t.Warning).OnClick(func() {
					if !a.setIssueSupplementAuto(ct, issue.ID, false) {
						a.setIssueActionNotice("Could not save the automatic-continuation stop.", "supplement-auto-stop-failed", "")
					}
				})
			} else if pending > 0 {
				secondaryButton(c, msgs.SupplementResumeAuto()).OnClick(func() {
					if !a.setIssueSupplementAuto(ct, issue.ID, true) {
						a.setIssueActionNotice("Could not save the automatic-continuation resume.", "supplement-auto-resume-failed", "")
					}
				})
			}
		}
	}
	if len(batches) > 0 {
		start := 0
		if !a.issueSupplementShowAllBatches && len(batches) > 6 {
			start = len(batches) - 6
		}
		if start > 0 {
			ui.Text(c, msgs.SupplementHiddenBatches(start)).
				FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		}
		for _, batch := range batches[start:] {
			if batch == nil {
				continue
			}
			batch := batch
			ui.Column(c).Padding(7).Radius(7).Background(t.Surface).Gap(2).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, issueSupplementBatchLabel(msgs, batch)).FontSize(typeAction).FontWeight(500).TextColor(t.TextMuted)
					ui.Spacer(c)
					ui.Text(c, msgs.SupplementTargetRevision(fmt.Sprint(batch.Target.RequirementsRevision))).
						FontSize(14).TextColor(t.TextMuted)
				})
				for _, item := range batch.Snapshot {
					item := item
					ui.Column(c).Gap(1).Children(func() {
						ui.Text(c, msgs.SupplementReceived(fmt.Sprint(item.RequirementsRevision), truncate(item.Text, 180))).
							FontSize(14).LineHeight(1.5).TextColor(t.Text)
						if a.issueSupplementPreviewID == item.ID {
							secondaryButton(c, msgs.SupplementClosePreview()).OnClick(func() { a.issueSupplementPreviewID = "" })
							markdownReader(c, item.Text, markdownReaderOptions{PlainText: true, CopyLabel: locales.In(c.Locale()).CopySupplementTextLabel()}).
								FontSize(14).LineHeight(1.5).TextColor(t.Text)
						} else {
							secondaryButton(c, msgs.SupplementPreviewFull()).OnClick(func() { a.issueSupplementPreviewID = item.ID })
						}
					})
				}
				if batch.SaveError != "" {
					ui.Text(c, msgs.SupplementReceiptFailed(batch.SaveError)).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
				}
				if batch.State == supplementBatchAccepted {
					ui.Text(c, msgs.SupplementSubmitted(shortExecutionID(batch.ExecutionID), batch.TaskID)).
						FontSize(14).TextColor(t.TextMuted)
				}
				if editable && (batch.State == supplementBatchRejected || batch.State == supplementBatchNotDispatched) {
					secondaryButton(c, msgs.SupplementRetry()).OnClick(func() {
						if !a.retryIssueSupplementBatch(ct, issue, batch) {
							if a.issueActionError == "" {
								a.setIssueActionNotice("Could not retry this supplement.", "supplement-retry-failed", "")
							}
						}
					})
				}
				if batch.State == supplementBatchUnknown || batch.State == supplementBatchDispatching {
					ui.Text(c, msgs.SupplementNeedsCheck()).FontSize(14).TextColor(t.Warning)
					if batch.ExecutionID != "" {
						executionID := batch.ExecutionID
						secondaryButton(c, msgs.SupplementCheck()).OnClick(func() {
							if !a.reconcileIssueExecution(issue.ID, executionID) {
								a.setIssueActionNotice("Could not check this supplement execution.", "supplement-check-failed", "")
							}
						})
					}
				}
			})
		}
		if len(batches) > 6 {
			if a.issueSupplementShowAllBatches {
				secondaryButton(c, msgs.SupplementShowRecent()).OnClick(func() { a.issueSupplementShowAllBatches = false })
			} else {
				secondaryButton(c, msgs.SupplementShowAll()).OnClick(func() { a.issueSupplementShowAllBatches = true })
			}
		}
	}
}

func shortExecutionID(id string) string {
	if len(id) <= 18 {
		return id
	}
	return id[:18] + "…"
}

func supplementBatchStateLabel(msgs locales.Messages, state string) string {
	switch state {
	case supplementBatchDispatching:
		return msgs.SupplementBatchDispatching()
	case supplementBatchAccepted:
		return msgs.SupplementBatchAccepted()
	case supplementBatchRejected:
		return msgs.SupplementBatchRejected()
	case supplementBatchNotDispatched:
		return msgs.SupplementBatchNotDispatched()
	case supplementBatchUnknown:
		return msgs.SupplementBatchUnknown()
	case supplementBatchCanceled:
		return msgs.SupplementBatchCanceled()
	default:
		return msgs.SupplementBatchGeneric(state)
	}
}

func issueSupplementBatchLabel(msgs locales.Messages, batch *issueSupplementBatch) string {
	return supplementBatchStateLabel(msgs, batch.State)
}
