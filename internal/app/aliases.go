package app

import (
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/internal/connection"
	"github.com/HeimaoLST/Nagi/internal/locale"
	"github.com/HeimaoLST/Nagi/internal/markdown"
	"github.com/HeimaoLST/Nagi/internal/materials"
	"github.com/HeimaoLST/Nagi/internal/pi"
	"github.com/HeimaoLST/Nagi/internal/store"
	"github.com/HeimaoLST/Nagi/internal/theme"
)

const (
	appearanceSystem = theme.AppearanceSystem
	appearanceLight  = theme.AppearanceLight
	appearanceDark   = theme.AppearanceDark

	defaultLeftPaneWidth  = theme.DefaultLeftPaneWidth
	minLeftPaneWidth      = theme.MinLeftPaneWidth
	maxLeftPaneWidth      = theme.MaxLeftPaneWidth
	defaultRightPaneWidth = theme.DefaultRightPaneWidth
	minRightPaneWidth     = theme.MinRightPaneWidth
	maxRightPaneWidth     = theme.MaxRightPaneWidth
	minCenterPaneWidth    = theme.MinCenterPaneWidth
	paneSeparatorWidth    = theme.PaneSeparatorWidth
	avatarRailWidth       = theme.AvatarRailWidth
	fullRailBreakpoint    = theme.FullRailBreakpoint
	avatarRailBreakpoint  = theme.AvatarRailBreakpoint
	minAvatarDetailWidth  = theme.MinAvatarDetailWidth
	effectiveCenterWidth  = theme.EffectiveCenterWidth

	lineBody             = theme.LineBody
	controlHit           = theme.ControlHit
	typeSettingsTitle    = theme.TypeSettingsTitle
	lineSettingsTitle    = theme.LineSettingsTitle
	lineCompact          = theme.LineCompact
	typeHeaderMeta       = theme.TypeHeaderMeta
	lineHeader           = theme.LineHeader
	conversationMaxWidth = theme.ConversationMaxWidth
	userMessageMaxWidth  = theme.UserMessageMaxWidth
	typeLabel            = theme.TypeLabel
	lineLabel            = theme.LineLabel
	lineMeta             = theme.LineMeta
	lineAction           = theme.LineAction
	typeNavigation       = theme.TypeNavigation
	lineNavigation       = theme.LineNavigation
	settingsSidebarWidth = theme.SettingsSidebarWidth
	settingsContentWidth = theme.SettingsContentWidth
	preferenceBreakpoint = theme.PreferenceBreakpoint
	typeContactTitle     = theme.TypeContactTitle
	typePageTitle        = theme.TypePageTitle
	typeGroupTitle       = theme.TypeGroupTitle
	typeBody             = theme.TypeBody
	typeAction           = theme.TypeAction
	typeMeta             = theme.TypeMeta
	typeRailName         = theme.TypeRailName
	typeRailSummary      = theme.TypeRailSummary

	iconChrome   = theme.IconChrome
	iconHeader   = theme.IconHeader
	iconPanel    = theme.IconPanel
	iconResponse = theme.IconResponse
	iconContext  = theme.IconContext
	iconPrimary  = theme.IconPrimary

	contactStoreVersion                 = store.ContactStoreVersion
	storeRecoveryNotInstalled           = store.StoreRecoveryNotInstalled
	storeRecoveryVerifiedInstalled      = store.StoreRecoveryVerifiedInstalled
	storeRecoveryInstallUnconfirmed     = store.StoreRecoveryInstallUnconfirmed
	activityQuestionOpen                = store.IssueQuestionOpen
	activityQuestionDeliveryDispatching = store.IssueQuestionDeliveryDispatching
	activityQuestionDeliveryUnknown     = store.IssueQuestionDeliveryUnknown
	activityStatusNotStarted            = store.IssueStatusNotStarted
	activityStatusInProgress            = store.IssueStatusInProgress
	activityStatusWaitingUser           = store.IssueStatusWaitingUser
	activityStatusNeedsAttention        = store.IssueStatusNeedsAttention
	activityStatusCompleted             = store.IssueStatusCompleted
	activityStatusCanceled              = store.IssueStatusCanceled
	requirementUnverified               = store.RequirementUnverified
	requirementSatisfied                = store.RequirementSatisfied
	requirementUnmet                    = store.RequirementUnmet
	activityExecutionPending            = store.IssueExecutionPending
	activityExecutionNotDispatched      = store.IssueExecutionNotDispatched
	activityExecutionRejected           = store.IssueExecutionRejected
	activityExecutionAccepted           = store.IssueExecutionAccepted
	activityExecutionRunning            = store.IssueExecutionRunning
	activityExecutionCompleted          = store.IssueExecutionCompleted
	activityExecutionFailed             = store.IssueExecutionFailed
	activityExecutionInterrupted        = store.IssueExecutionInterrupted
	activityExecutionUnknown            = store.IssueExecutionUnknown
	supplementStateDispatching          = store.SupplementStateDispatching
	supplementStateUnknown              = store.SupplementStateUnknown
	supplementBatchDispatching          = store.SupplementBatchDispatching
	supplementBatchAccepted             = store.SupplementBatchAccepted
	supplementBatchRejected             = store.SupplementBatchRejected
	supplementBatchNotDispatched        = store.SupplementBatchNotDispatched
	supplementBatchUnknown              = store.SupplementBatchUnknown
	supplementBatchCanceled             = store.SupplementBatchCanceled
	activityToolActionCreate            = store.IssueToolActionCreate
	activityToolActionUpdate            = store.IssueToolActionUpdate
	activityToolActionRun               = store.IssueToolActionRun
	activityToolActionWait              = store.IssueToolActionWait
	activityToolReceiptAccepted         = store.IssueToolReceiptAccepted
	activityToolReceiptUnknown          = store.IssueToolReceiptUnknown
	activityToolModeWork                = store.IssueToolModeWork
	activityToolExecutionPageLimit      = store.IssueToolExecutionPageLimit

	connectionAPIResponses          = connection.APIResponses
	connectionAPIChatCompletions    = connection.APIChatCompletions
	connectionValidationEndpointURL = connection.ValidationEndpointURL

	taskContextMaxBytes = materials.MaxBytes
)

type (
	workbenchTheme       = theme.WorkbenchTheme
	selectOption         = theme.SelectOption
	iconControlRole      = theme.IconControlRole
	appearanceThemeIDs   = theme.AppearanceThemeIDs
	themeFileError       = theme.ThemeFileError
	themeContrastWarning = theme.ThemeContrastWarning
	customTheme          = theme.CustomTheme
	customThemeCatalog   = theme.CustomThemeCatalog

	markdownReaderOptions = markdown.ReaderOptions
	markdownMessageBubble = markdown.MessageBubble
	markdownLinkTarget    = markdown.LinkTarget

	taskContextItem             = store.TaskContextItem
	pendingSend                 = store.PendingSend
	piSelection                 = store.PiSelection
	piConfiguration             = store.PiConfiguration
	piConversationBinding       = store.PiConversationBinding
	piActivityBinding           = store.PiActivityBinding
	piActivityRun               = store.PiActivityRun
	piQuestionCall              = store.PiQuestionCall
	activityQuestion            = store.IssueQuestion
	activityRequirement         = store.IssueRequirement
	activityExecution           = store.IssueExecution
	productActivityIntent       = store.ProductActivityIntent
	activityHandoff             = store.IssueHandoff
	activitySupplementBatch     = store.IssueSupplementBatch
	activitySupplementLedger    = store.IssueSupplementLedger
	activityToolScope           = store.IssueToolScope
	activityToolError           = store.IssueToolError
	activityToolActionRequest   = store.IssueToolActionRequest
	activityToolReceipt         = store.IssueToolReceipt
	activityToolOperation       = store.IssueToolOperation
	activityToolOperationLedger = store.IssueToolOperationLedger
	activityToolListResult      = store.IssueToolIssueListResult
	activityToolSummaryView     = store.IssueToolIssueSummary
	activityToolRequirementView = store.IssueToolRequirementView
	activityToolExecutionView   = store.IssueToolExecutionView
	activityToolDetail          = store.IssueToolIssueDetail
	storedContact               = store.StoredContact
	storedActivityExecution     = store.StoredIssueExecution
	storedRequirement           = store.StoredRequirement
	storedActivity              = store.StoredIssue
	storedLayout                = store.StoredLayout
	storedState                 = store.StoredState
	contactStore                = store.ContactStore
	productPendingSend          = store.ProductPendingSend
	productUIState              = store.ProductUIState
	storeRecoveryCopy           = store.StoreRecoveryCopy
	storeRecoveryTarget         = store.StoreRecoveryTarget
	storeRecoveryResult         = store.StoreRecoveryResult

	connectionConfig = connection.Config

	taskContext = materials.Context
)

var (
	themeColorFields = theme.ColorFields

	glyphCloseLine     = theme.GlyphCloseLine
	glyphMoreLine      = theme.GlyphMoreLine
	glyphSettings4Line = theme.GlyphSettings4Line
	navBackIcon        = theme.NavBackIcon
	navContactsIcon    = theme.NavContactsIcon
	composerAttachIcon = theme.ComposerAttachIcon
	composerSendIcon   = theme.ComposerSendIcon
	composerStopIcon   = theme.ComposerStopIcon
	railHideIcon       = theme.RailHideIcon
	railAddIcon        = theme.RailAddIcon
	railSettingsIcon   = theme.RailSettingsIcon

	errActivityToolAction       = store.ErrIssueToolAction
	errActivityToolSaveRequired = store.ErrIssueToolSaveRequired

	errTaskContextName = materials.ErrName
	errTaskContextRead = materials.ErrRead
)

func readSavedLanguageTag() string { return locale.ReadSavedLanguageTag() }

func piPayloadPaths() (string, string, error) { return pi.PayloadPaths() }

func resolveWorkbenchTheme(dark bool) workbenchTheme { return theme.ResolveWorkbenchTheme(dark) }
func paletteContrast(a, b ui.Color) float64          { return theme.PaletteContrast(a, b) }
func resolveBotAvatar(t *ui.Theme, id, name string, builtin bool) theme.BotAvatar {
	return theme.ResolveBotAvatar(t, id, name, builtin)
}
func segmentedSelection(c *ui.Context, selected *int, labels ...string) ui.Element {
	return theme.SegmentedSelection(c, selected, labels...)
}
func preferenceSelect(c *ui.Context, selected *string, options []selectOption, label string, disabled ...bool) ui.Element {
	return theme.PreferenceSelect(c, selected, options, label, disabled...)
}
func appTooltip(c *ui.Context, anchor ui.Element, text string) ui.Element {
	return theme.AppTooltip(c, anchor, text)
}
func secondaryButton(c *ui.Context, label string) ui.Element {
	return theme.SecondaryButton(c, label)
}
func primaryButton(c *ui.Context, label string) ui.Element { return theme.PrimaryButton(c, label) }
func dangerButton(c *ui.Context, label string) ui.Element  { return theme.DangerButton(c, label) }
func linkButton(c *ui.Context, label string) ui.Element    { return theme.LinkButton(c, label) }
func navigationControl(c *ui.Context, e ui.Element, palette workbenchTheme, selected bool) ui.Element {
	return theme.NavigationControl(c, e, palette, selected)
}
func sharedControlFace(c *ui.Context, e ui.Element, region float32, exponent float64, outlined, invalid, selected bool, inputBorder ...ui.Color) ui.Element {
	return theme.SharedControlFace(c, e, region, exponent, outlined, invalid, selected, inputBorder...)
}
func drawControlFace(c *ui.Context, e ui.Element, region float32, exponent float64, outlined, invalid, selected bool, insetX, insetY float32, focusFill ui.Color, inputBorder ...ui.Color) ui.Element {
	return theme.DrawControlFace(c, e, region, exponent, outlined, invalid, selected, insetX, insetY, focusFill, inputBorder...)
}
func iconControlFace(c *ui.Context, e ui.Element, role iconControlRole, selected bool) ui.Element {
	return theme.IconControlFace(c, e, role, selected)
}
func controlIcon(c *ui.Context, glyph *ui.SVG, role iconControlRole) ui.Element {
	return theme.ControlIcon(c, glyph, role)
}
func pageHeading(c *ui.Context, title string) ui.Element { return theme.PageHeading(c, title) }
func formTextArea(c *ui.Context, value *string, invalid bool, inputBorder ...ui.Color) ui.Element {
	return theme.FormTextArea(c, value, invalid, inputBorder...)
}
func preferenceInput(c *ui.Context, value *string, invalid bool, inputBorder ...ui.Color) ui.Element {
	return theme.PreferenceInput(c, value, invalid, inputBorder...)
}
func preferenceGroup(c *ui.Context, t *ui.Theme, children func()) {
	theme.PreferenceGroup(c, t, children)
}
func preferenceSection(c *ui.Context, t *ui.Theme, title string, children func()) {
	theme.PreferenceSection(c, t, title, children)
}
func settingsPreferenceRow(c *ui.Context, t *ui.Theme, label, description string, control func(), last ...bool) {
	theme.SettingsPreferenceRow(c, t, label, description, control, last...)
}
func loadCustomThemes(directory string) customThemeCatalog {
	return theme.LoadCustomThemeCatalog(directory)
}
func installCustomTheme(sourcePath, directory string) (customTheme, *themeFileError) {
	return theme.ImportCustomThemeFile(sourcePath, directory)
}

func markdownReader(c *ui.Context, source string, options markdownReaderOptions) ui.Element {
	return markdown.Reader(c, source, options)
}
func recoveryNoticeView(c *ui.Context, t *ui.Theme, summary, detail string, expanded bool, toggle, settings func(), settingsLabel string, actions ...func()) ui.Element {
	return markdown.RecoveryNotice(c, t, summary, detail, expanded, toggle, settings, settingsLabel, actions...)
}
func truncate(text string, max int) string { return markdown.Truncate(text, max) }
func safeDiagnostic(text string) string    { return markdown.SafeDiagnostic(text) }

func cloneBuiltinTools(selection *[]string) *[]string { return store.CloneBuiltinTools(selection) }
func cloneTaskContextItems(items []taskContextItem) []taskContextItem {
	return store.CloneTaskContextItems(items)
}
func clonePendingSend(send *pendingSend) *pendingSend { return store.ClonePendingSend(send) }
func clonePiConversationBinding(b *piConversationBinding) *piConversationBinding {
	return store.ClonePiConversationBinding(b)
}
func clonePiConversationBindings(b []piConversationBinding) []piConversationBinding {
	return store.ClonePiConversationBindings(b)
}
func clonePiActivityBinding(b *piActivityBinding) *piActivityBinding {
	return store.ClonePiActivityBinding(b)
}
func clonePiActivityRun(r *piActivityRun) *piActivityRun { return store.ClonePiActivityRun(r) }
func clonePiQuestionCall(c *piQuestionCall) *piQuestionCall {
	return store.ClonePiQuestionCall(c)
}
func cloneActivityHandoff(h *activityHandoff) *activityHandoff { return store.CloneIssueHandoff(h) }
func cloneActivitySupplementLedger(l *activitySupplementLedger) *activitySupplementLedger {
	return store.CloneIssueSupplementLedger(l)
}
func newContactID() string                                   { return store.NewContactID() }
func nowMillis() int64                                       { return store.NowMillis() }
func newActivityID() string                                  { return store.NewIssueID() }
func newActivitySupplementLedger() *activitySupplementLedger { return store.NewIssueSupplementLedger() }
func parseActivityRequirements(text string) []*activityRequirement {
	return store.ParseIssueRequirements(text)
}
func openContactStore(path, defaultWorkspace string) (*contactStore, error) {
	return store.OpenContactStore(path, defaultWorkspace)
}
func productUIPath(profilePath string) string { return store.ProductUIPath(profilePath) }
func readProductUI(profilePath string) (*productUIState, error) {
	return store.ReadProductUI(profilePath)
}
func writeProfileJSON(path string, value any) error { return store.WriteProfileJSON(path, value) }
func inspectStoreRecoveryCopy(path string) (*storeRecoveryCopy, error) {
	return store.InspectStoreRecoveryCopy(path)
}
func writeStoreRecoveryCopy(copy *storeRecoveryCopy, destination string) (string, error) {
	return store.WriteStoreRecoveryCopy(copy, destination)
}
func inspectStoreRecoveryTarget(path string) (*storeRecoveryTarget, error) {
	return store.InspectStoreRecoveryTarget(path)
}
func restoreStoreRecoveryCopy(copy *storeRecoveryCopy, target *storeRecoveryTarget) (storeRecoveryResult, error) {
	return store.RestoreStoreRecoveryCopy(copy, target)
}
func restoreStoreRecoveryCopyWithRename(copy *storeRecoveryCopy, target *storeRecoveryTarget, rename func(string, string) error) (storeRecoveryResult, error) {
	return store.RestoreStoreRecoveryCopyWithRename(copy, target, rename)
}
func validateActivityToolReceipt(receipt *activityToolReceipt) error {
	return store.ValidateIssueToolReceipt(receipt)
}

func loadConnectionConfig(path string) (connectionConfig, error) {
	return connection.LoadConfig(path)
}
func saveConnectionConfig(path string, config connectionConfig) error {
	return connection.SaveConfig(path, config)
}
func readPrivateConnectionConfig(path string) (connection.PrivateConfig, error) {
	return connection.ReadPrivateConfig(path)
}
func validateConnectionConfig(config connectionConfig) map[string]string {
	return connection.ValidateConfig(config)
}
func connectionValidationSummary(fields map[string]string) string {
	return connection.ValidationSummary(fields)
}

func validateTaskContext(items []taskContextItem) error { return materials.Validate(items) }
func taskContextReadOnlyView(c *ui.Context, theme workbenchTheme, items []taskContextItem) {
	materials.ReadOnlyView(c, theme, items)
}
