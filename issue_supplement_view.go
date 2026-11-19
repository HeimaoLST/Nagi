package main

import (
	"github.com/HeimaoLST/Nagi/locales"
)

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
