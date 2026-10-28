package webdavarchive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

func cleanupPreviewToken(preview CleanupPreview) string {
	hash := sha256.New()
	smsIDs := append([]uint(nil), preview.EligibleSMSIDs...)
	sort.Slice(smsIDs, func(i, j int) bool { return smsIDs[i] < smsIDs[j] })
	for _, id := range smsIDs {
		_, _ = fmt.Fprintf(hash, "sms:%d:%s\n", id, preview.SMSChecksums[id])
	}
	callIDs := append([]uint64(nil), preview.EligibleCallIDs...)
	sort.Slice(callIDs, func(i, j int) bool { return callIDs[i] < callIDs[j] })
	for _, id := range callIDs {
		_, _ = fmt.Fprintf(hash, "call:%d:%s\n", id, preview.CallChecksums[id])
	}
	paths := append([]string(nil), preview.RecordingFiles...)
	sort.Strings(paths)
	for _, path := range paths {
		_, _ = fmt.Fprintf(hash, "recording:%s:%s\n", path, preview.RecordingChecksums[path])
	}
	return hex.EncodeToString(hash.Sum(nil))
}
