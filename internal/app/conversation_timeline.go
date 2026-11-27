package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/theme"
	"github.com/HeimaoLST/Nagi/locales"
)

func unsupportedOrchestrationMethod(err error) bool {
	var remote *pi.RemoteError
	if !errors.As(err, &remote) {
		return false
	}
	message := strings.ToLower(remote.Message)
	return remote.Code == "unknown_method" || strings.Contains(message, "unknown method") ||
		strings.Contains(message, "unsupported method") || strings.Contains(message, "unrecognized method")
}

func productTimeline(ctx context.Context, client *pi.ProductClient, id string) (historyProjection, error) {
	params := pi.ProductConversationTimelineParams{ConversationID: id}
	var projection historyProjection
	var reply strings.Builder
	seenNotices := make(map[string]struct{})
	for {
		page, err := client.ConversationTimeline(ctx, params)
		if err != nil {
			if params.Cursor == "" && unsupportedOrchestrationMethod(err) {
				return productHistory(ctx, client, id)
			}
			return historyProjection{}, err
		}
		projection.transcriptError = projection.transcriptError || page.TranscriptError != ""
		for _, row := range page.Items {
			switch row.Kind {
			case "message":
				projection.appendProductMessage(&reply, row.MessageID, row.Role, row.Timestamp, row.Text, row.Materials, row.Quote)
			case "notice":
				if row.Notice == nil {
					continue
				}
				if _, seen := seenNotices[row.MessageID]; seen {
					continue
				}
				seenNotices[row.MessageID] = struct{}{}
				stamp, _ := time.Parse(time.RFC3339Nano, row.Timestamp)
				projection.messages = append(projection.messages, &message{
					Key: "notice:" + row.MessageID, Kind: msgNotice, Text: row.Text, Timestamp: stamp,
					Notice: row.Notice, AnchorSeq: row.AnchorSeq,
				})
			}
		}
		if page.NextCursor == nil {
			return projection, nil
		}
		params.Cursor = *page.NextCursor
	}
}

func (p *historyProjection) appendProductMessage(reply *strings.Builder, id, role, timestamp, text string, attachments []pi.ProductMaterial, quote *pi.ProductQuote) {
	kind, from := msgContact, "contact"
	if role == "user" {
		kind, from = msgUser, "me"
	}
	stamp, _ := time.Parse(time.RFC3339Nano, timestamp)
	when := formatMillis(stamp.UnixMilli())
	materials := productTaskContext(attachments)
	if n := len(p.messages); kind == msgContact && n > 0 && p.messages[n-1].Kind == msgContact {
		previous := p.messages[n-1]
		if reply.Len() == 0 {
			reply.WriteString(previous.Text)
		}
		reply.WriteString(text)
		previous.Text = reply.String()
	} else {
		reply.Reset()
		p.messages = append(p.messages, &message{
			Key: id, MessageID: id, Kind: kind, Text: text, Time: when, Timestamp: stamp,
			Context: materials, Quote: quote, History: true, ReadByBot: kind == msgUser,
		})
	}
	p.entries = append(p.entries, historyEntry{ID: id, Date: when, From: from, Text: text, Context: materials})
}

func (a *appModel) timelineNotice(c *ui.Context, t *ui.Theme, ownerID string, item *message) {
	msgs, notice := locales.In(c.Locale()), item.Notice
	if notice == nil || notice.BotID != ownerID {
		return
	}
	badge, ink := msgs.TimelineNoticeBadge(), t.TextMuted
	if notice.Urgency == "high" || notice.Urgency == "urgent" {
		badge, ink = msgs.TimelineNoticeUrgent(), t.Warning
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(4).Padding(8, 12).Radius(theme.CardRadius).Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, badge).FontSize(typeMeta).FontWeight(500).FixedLineHeight(lineMeta).TextColor(ink)
			if notice.Dormant {
				ui.Text(c, msgs.TimelineNoticeReadOnly()).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
			if !item.Timestamp.IsZero() {
				ui.Text(c, formatChatTimestamp(item.Timestamp, c.Now(), c.Locale())).FontSize(typeMeta).FixedLineHeight(lineMeta).TextColor(t.TextMuted)
			}
		})
		ui.Text(c, item.Text).MinWidth(0).Selectable().FontSize(typeBody).LineHeight(1.5).TextColor(t.Text)
		if len(notice.SourceRefs) > 0 {
			ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
				for _, ref := range notice.SourceRefs[:min(4, len(notice.SourceRefs))] {
					label := truncate(ref.Label, 80)
					if label == "" {
						label = orchestrationSourceLabel(msgs, pi.ProductMandateSourceKind(ref.Kind))
					}
					ui.Text(c, label).MinWidth(0).FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
				}
			})
		}
		if notice.MandateID != "" && a.productProfileReady() && !a.dataRecoveryBlocksChanges() {
			button := theme.TextAction(c, msgs.TimelineInspectMandate(), false).AlignSelf(ui.Start)
			a.focusBackgroundOpener(button, item.Key)
			button.OnClick(func() { a.openBackgroundWork(ownerID, notice.MandateID, item.Key) })
		}
	})
}

func timelineTranscriptWarning(c *ui.Context, t *ui.Theme) {
	ui.Row(c).FillWidth().MinWidth(0).Justify(ui.Center).Children(func() {
		ui.Text(c, locales.In(c.Locale()).TimelineTranscriptErrorNotice()).MinWidth(0).
			MaxWidth(conversationMaxWidth).Padding(6, 16).FontSize(typeMeta).LineHeight(1.5).TextColor(t.Warning)
	})
}
