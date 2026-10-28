package api

import (
	"context"
	"errors"

	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/phonebook"
)

type contactImportInput struct {
	Name, DeviceID string
	Data           []byte
}
type contactImportFailure struct {
	Code   string
	Status int
	Err    error
}

func (e *contactImportFailure) Error() string { return e.Err.Error() }
func (e *contactImportFailure) Unwrap() error { return e.Err }
func (s *Server) importContactContent(ctx context.Context, input contactImportInput) (map[string]any, error) {
	parsed := phonebook.Parse(input.Data, input.Name)
	if len(parsed) == 0 {
		return nil, &contactImportFailure{Code: "empty_import", Status: 400, Err: errors.New("文件里没有识别到联系人。请用手机或 Google 通讯录导出的 vcf / csv（iOS、Google、三星、小米、华为、OPPO、vivo 都可以）")}
	}
	if len(parsed) > maxContactBatch {
		return nil, &contactImportFailure{Code: "batch_too_large", Status: 400, Err: errors.New("一次最多导入 5000 条，请拆分文件后重试")}
	}
	inputs := make([]db.PhoneContactInput, 0, len(parsed))
	region := s.phoneNumberRegion(input.DeviceID)
	for _, item := range parsed {
		inputs = append(inputs, db.PhoneContactInput{Number: item.Number, Name: item.Name, Region: region, GroupKey: item.ContactID})
	}
	imported, skipped, err := db.UpsertPhoneContactsWithRegion(ctx, inputs, region)
	if err != nil {
		return nil, &contactImportFailure{Code: "contact_import_failed", Status: 500, Err: err}
	}
	return map[string]any{"imported": imported, "skipped": skipped, "parsed": len(parsed)}, nil
}
