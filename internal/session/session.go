// Package session is a thin re-export shim over github.com/sausheong/harness/session.
// Felix consumers continue to import "internal/session" for Session, Store,
// SessionEntry, MessageData, ToolCallData, ToolResultData, CompactionData,
// ImageData, and the constructor helpers.
package session

import harness "github.com/sausheong/harness/session"

type (
	EntryType        = harness.EntryType
	SessionEntry     = harness.SessionEntry
	ImageData        = harness.ImageData
	MessageData      = harness.MessageData
	ToolCallData     = harness.ToolCallData
	ToolResultData   = harness.ToolResultData
	CompactionData   = harness.CompactionData
	Session          = harness.Session
	SessionInfo      = harness.SessionInfo
	Store            = harness.Store
)

const (
	EntryTypeMessage    = harness.EntryTypeMessage
	EntryTypeToolCall   = harness.EntryTypeToolCall
	EntryTypeToolResult = harness.EntryTypeToolResult
	EntryTypeMeta       = harness.EntryTypeMeta
	EntryTypeCompaction = harness.EntryTypeCompaction
)

var (
	NewSession                   = harness.NewSession
	NewStore                     = harness.NewStore
	UserMessageEntry             = harness.UserMessageEntry
	UserMessageWithImagesEntry   = harness.UserMessageWithImagesEntry
	AssistantMessageEntry        = harness.AssistantMessageEntry
	ToolCallEntry                = harness.ToolCallEntry
	ToolResultEntry              = harness.ToolResultEntry
	AbortedToolResultEntry       = harness.AbortedToolResultEntry
	CompactionEntry              = harness.CompactionEntry
)
