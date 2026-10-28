package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/yibaiba/hideck/internal/global"

	"github.com/spf13/viper"
	"github.com/yibaiba/hideck/pkg/logger"

	"github.com/gin-gonic/gin"
)

func (s *Server) useSystemInfo(c businessContext) {
	c.JSON(http.StatusOK, gin.H{
		"version":    global.Version,
		"build_time": global.BuildTime,
		"config":     viper.ConfigFileUsed(),
		"docs":       currentAPIDocsLinks(),
	})
}

func (s *Server) useLogHistory(c businessContext) {
	// 读取参数
	lines := 500 // 默认返回最近 500 行
	if n := c.Query("lines"); n != "" {
		if v, err := strconv.Atoi(n); err == nil && v > 0 && v <= 2000 {
			lines = v
		}
	}

	// 日志文件路径（使用 logger 的默认路径）
	logFile := "logs/app.log"

	recentLines, err := readLastLines(logFile, lines, 1<<20)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"logs": []logger.LogEntry{}, "error": "无法读取日志文件"})
		return
	}

	// 解析日志行
	entries := make([]logger.LogEntry, 0, len(recentLines))
	for _, line := range recentLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry := parseLogLine(line)
		if entry.Message != "" {
			entries = append(entries, entry)
		}
	}

	c.JSON(http.StatusOK, gin.H{"logs": entries})
}
