package api

import (
	"context"
	"net/http"
	"time"

	"github.com/yibaiba/hideck/internal/device"
)

const operatorScanProgressInterval = time.Second

// The existing worker owns the scan and its timeout. Cancelling a caller stops
// waiting; it does not claim that an already-started modem scan was rolled back.
func awaitOperatorScan(ctx context.Context, worker *device.Worker, report func(string, float64) error) (device.OperatorScanResult, error) {
	result := worker.StartOrGetOperatorScan(ctx)
	ticker := time.NewTicker(operatorScanProgressInterval)
	defer ticker.Stop()
	for result.Status == device.OperatorScanStatusRunning {
		if err := report(result.Message, -1); err != nil {
			return result, err
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-ticker.C:
			result = worker.GetOperatorScanSnapshot()
		}
	}
	return result, nil
}
func runMCPScan(call *businessCall, worker *device.Worker) {
	result, err := awaitOperatorScan(call.ctx, worker, call.progress)
	if err != nil {
		call.JSON(http.StatusRequestTimeout, map[string]any{"error": err.Error()})
		return
	}
	status, body := operatorScanHTTPStatusAndBody(result)
	if result.Status == device.OperatorScanStatusFailed {
		status = http.StatusBadGateway
	}
	call.JSON(status, body)
}
