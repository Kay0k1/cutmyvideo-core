//go:build !linux

package app

import "os"

const draftCacheHintSupported = false

func discardCompletedDraftCache(*os.File) {}
