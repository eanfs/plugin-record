package record

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/shirou/gopsutil/v3/disk"
	"go.uber.org/zap"
)

// httpClient 带超时的 HTTP 客户端
var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

// 向第三方发送异常报警
func SendToThirdPartyAPI(exception *Exception) {
	exception.CreateTime = time.Now().Format("2006-01-02 15:04:05")
	exception.ServerIP = RecordPluginConfig.LocalIp
	data, err := json.Marshal(exception)
	if err != nil {
		plugin.Error("序列化异常信息失败", zap.Error(err))
		return
	}
	err = db.Create(&exception).Error
	if err != nil {
		plugin.Error("异常数据插入数据库失败", zap.Error(err))
		return
	}
	resp, err := httpClient.Post(RecordPluginConfig.ExceptionPostUrl, "application/json", bytes.NewBuffer(data))
	if err != nil {
		plugin.Error("发送异常信息到第三方API失败", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		plugin.Error("发送异常信息失败", zap.Int("statusCode", resp.StatusCode))
	} else {
		plugin.Info("异常信息发送成功")
	}
}

// 磁盘超上限报警
func getDiskException(streamPath string) bool {
	d, err := disk.Usage("/")
	if err != nil {
		return false
	}
	if d.UsedPercent >= RecordPluginConfig.DiskMaxPercent {
		exceptionChannel <- &Exception{AlarmType: "disk alarm", AlarmDesc: "disk is full", StreamPath: streamPath}
		return true
	}
	return false
}
