package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/mbn"
)

func (s *Server) registerMBNRoutes(api *gin.RouterGroup) {
	api.GET("/mbn/files", s.handleMBNFiles)
	api.POST("/mbn/files", s.handleMBNUpload)
	api.GET("/mbn/files/:file_id", s.handleMBNDownload)
	api.DELETE("/mbn/files/:file_id", s.handleMBNDeleteFile)
	api.GET("/devices/:device_id/mbn", s.handleMBNStatus)
	api.GET("/devices/:device_id/mbn/operations", s.handleMBNOperations)
	api.POST("/devices/:device_id/mbn/operations", s.handleMBNStart)
	api.POST("/devices/:device_id/mbn/operations/:operation_id/cancel", s.handleMBNCancel)
	api.GET("/devices/:device_id/mbn/policies", s.handleMBNPolicies)
	api.PUT("/devices/:device_id/mbn/policies", s.handleMBNPolicy)
}

func (s *Server) mbnForRequest(c businessContext) *mbn.Service {
	if s.pool == nil || s.pool.MBNService() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MBN 服务未初始化"})
		return nil
	}
	return s.pool.MBNService()
}
func mbnError(c businessContext, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, mbn.ErrConflict) || errors.Is(err, mbn.ErrOwnerChanged) {
		status = http.StatusConflict
	}
	if errors.Is(err, mbn.ErrNotFound) {
		status = http.StatusNotFound
	}
	c.JSON(status, gin.H{"error": err.Error()})
}
func (s *Server) handleMBNStatus(c *gin.Context) {
	s.useMBNStatus(c)
}

func (s *Server) useMBNStatus(c businessContext) {
	service := s.mbnForRequest(c)
	if service == nil {
		return
	}
	status, err := service.Inspect(businessRequestContext(c), c.Param("device_id"))
	if err != nil {
		mbnError(c, err)
		return
	}
	c.JSON(http.StatusOK, status)
}
func (s *Server) handleMBNOperations(c *gin.Context) {
	s.useMBNOperations(c)
}

func (s *Server) useMBNOperations(c businessContext) {
	service := s.mbnForRequest(c)
	if service == nil {
		return
	}
	ops, err := service.Operations(businessRequestContext(c), c.Param("device_id"))
	if err != nil {
		mbnError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": ops})
}
func (s *Server) handleMBNStart(c *gin.Context) {
	s.useMBNStart(c)
}

func (s *Server) useMBNStart(c businessContext) {
	service := s.mbnForRequest(c)
	if service == nil {
		return
	}
	var request mbn.Request
	if err := c.ShouldBindJSON(&request); err != nil {
		mbnError(c, err)
		return
	}
	if request.ExpectedOwner == nil {
		mbnError(c, errors.New("请先读取模组，并提交 expected_owner 防止对换卡后的设备执行旧操作"))
		return
	}
	op, err := service.Start(businessRequestContext(c), c.Param("device_id"), request)
	if err != nil {
		mbnError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"operation_id": op.ID, "operation": op})
}
func (s *Server) handleMBNCancel(c *gin.Context) {
	s.useMBNCancel(c)
}

func (s *Server) useMBNCancel(c businessContext) {
	service := s.mbnForRequest(c)
	if service == nil {
		return
	}
	op, err := service.Operation(businessRequestContext(c), c.Param("operation_id"))
	if err != nil {
		mbnError(c, err)
		return
	}
	if op.DeviceID != c.Param("device_id") {
		mbnError(c, mbn.ErrNotFound)
		return
	}
	op, err = service.Cancel(businessRequestContext(c), op.ID)
	if err != nil {
		mbnError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, op)
}
func (s *Server) handleMBNFiles(c *gin.Context) {
	s.useMBNFiles(c)
}

func (s *Server) useMBNFiles(c businessContext) {
	service := s.mbnForRequest(c)
	if service == nil {
		return
	}
	files, err := service.Assets(businessRequestContext(c))
	if err != nil {
		mbnError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": files})
}
func (s *Server) handleMBNUpload(c *gin.Context) {
	service := s.mbnForRequest(c)
	if service == nil {
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		mbnError(c, err)
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	handle, err := file.Open()
	if err != nil {
		mbnError(c, err)
		return
	}
	data, readErr := io.ReadAll(handle)
	if err := errors.Join(readErr, handle.Close()); err != nil {
		mbnError(c, err)
		return
	}
	asset, err := service.Upload(c.Request.Context(), mbn.Asset{Name: file.Filename, Model: c.PostForm("model"), Firmware: c.PostForm("firmware"), Source: c.PostForm("source")}, data)
	if err != nil {
		mbnError(c, err)
		return
	}
	c.JSON(http.StatusCreated, asset)
}
func (s *Server) handleMBNDownload(c *gin.Context) {
	s.useMBNDownload(c)
}

func (s *Server) useMBNDownload(c businessContext) {
	service := s.mbnForRequest(c)
	if service == nil {
		return
	}
	_, data, err := service.Asset(businessRequestContext(c), c.Param("file_id"))
	if err != nil {
		mbnError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="profile.mbn"`)
	c.Data(http.StatusOK, "application/octet-stream", data)
}
func (s *Server) handleMBNDeleteFile(c *gin.Context) {
	s.useMBNDeleteFile(c)
}

func (s *Server) useMBNDeleteFile(c businessContext) {
	service := s.mbnForRequest(c)
	if service == nil {
		return
	}
	if err := service.DeleteAsset(businessRequestContext(c), c.Param("file_id")); err != nil {
		mbnError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (s *Server) handleMBNPolicies(c *gin.Context) {
	s.useMBNPolicies(c)
}

func (s *Server) useMBNPolicies(c businessContext) {
	service := s.mbnForRequest(c)
	if service == nil {
		return
	}
	w := s.pool.GetWorker(c.Param("device_id"))
	if w == nil {
		mbnError(c, mbn.ErrOwnerChanged)
		return
	}
	policies, err := service.Policies(businessRequestContext(c))
	if err != nil {
		mbnError(c, err)
		return
	}
	items := []mbn.Policy{}
	for _, p := range policies {
		if p.Key == "sim:"+db.CanonicalICCID(w.CurrentICCID()) || p.Key == "device:"+w.Config.ModemIMEI {
			items = append(items, p)
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
func (s *Server) handleMBNPolicy(c *gin.Context) {
	s.useMBNPolicy(c)
}

func (s *Server) useMBNPolicy(c businessContext) {
	service := s.mbnForRequest(c)
	if service == nil {
		return
	}
	id := c.Param("device_id")
	release, err := s.pool.AcquireDeviceOperation(id)
	if err != nil {
		mbnError(c, err)
		return
	}
	defer release()
	var p mbn.Policy
	if err := c.ShouldBindJSON(&p); err != nil {
		mbnError(c, err)
		return
	}
	w := s.pool.GetWorker(id)
	if w == nil {
		mbnError(c, mbn.ErrOwnerChanged)
		return
	}
	if p.Key != "sim:"+db.CanonicalICCID(w.CurrentICCID()) && p.Key != "device:"+w.Config.ModemIMEI {
		mbnError(c, mbn.ErrOwnerChanged)
		return
	}
	if err := service.SavePolicy(businessRequestContext(c), p); err != nil {
		mbnError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"saved": true, "message": "策略已保存，将在下一次身份就绪或重连时应用"})
}

// Long-running MBN jobs retain logical device ownership while allowing status
// reads and USB discovery. The modem's binary queue separately owns its bytes.
func (s *Server) mbnMutationMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.isSharedBusinessRoute(c.FullPath()) || s.pool == nil || c.Request.Method == http.MethodGet || strings.Contains(c.Request.URL.Path, "/mbn") {
			c.Next()
			return
		}
		id := c.Param("device_id")
		ids := []string{}
		if id != "" {
			ids = append(ids, id)
		}
		if iccid := db.CanonicalICCID(c.Param("iccid")); iccid != "" {
			for _, w := range s.pool.GetAllWorkers() {
				if db.CanonicalICCID(w.CurrentICCID()) == iccid {
					ids = append(ids, w.ID)
				}
			}
		}
		for _, deviceID := range ids {
			release, err := s.pool.AcquireDeviceOperation(deviceID)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "MBN_BUSY"})
				return
			}
			defer release()
		}
		c.Next()
	}
}
