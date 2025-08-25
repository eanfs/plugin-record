package record

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// CheckRecordDB starts a goroutine to periodically check for stopped recordings.
func (conf *RecordConfig) CheckRecordDB() {
	go func() {
		plugin.Info("录制检查器已启动")
		ticker := time.NewTicker(60 * time.Second)
		defer func() {
			ticker.Stop()
			plugin.Info("录制检查器已停止")
		}()

		for range ticker.C {
			plugin.Info("开始检查录制状态", zap.Time("time", time.Now()))
			conf.checkAndHandleStoppedRecordings()
		}
	}()
}

// checkAndHandleStoppedRecordings fetches recording info from DB, compares with in-memory state, and handles stopped ones.
func (conf *RecordConfig) checkAndHandleStoppedRecordings() {
	eventRecords, err := getActiveRecordingsFromDB()
	if err != nil {
		plugin.Error("从数据库获取有效录制信息失败", zap.Error(err))
		return
	}
	plugin.Info("数据库中处于录制状态的流数量", zap.Int("count", len(eventRecords)))

	activeRecorders := conf.getActiveRecorders()
	stoppedRecordings := findStoppedRecordings(eventRecords, activeRecorders)

	if len(stoppedRecordings) > 0 {
		plugin.Info("发现已停止的录制流", zap.Int("count", len(stoppedRecordings)))
		for _, record := range stoppedRecordings {
			plugin.Info("已停止录制的视频流，尝试自动重启", zap.String("StreamPath", record.StreamPath), zap.String("Filename", record.Filename))
			// 自动重启录制
			switch record.Type {
			case "flv":
				var mode RecordMode
				if record.RecordMode == "1" {
					mode = EventMode
				} else {
					mode = OrdinaryMode
				}
				go NewFLVRecorder(mode).Start(record.StreamPath)
			case "mp4":
				go NewMP4Recorder().Start(record.StreamPath)
			case "fmp4":
				go NewFMP4Recorder().Start(record.StreamPath)
			case "hls":
				go NewHLSRecorder().Start(record.StreamPath)
			case "raw":
				go NewRawRecorder().Start(record.StreamPath)
			case "raw_audio":
				go NewRawAudioRecorder().Start(record.StreamPath)
			default:
				plugin.Warn("未知的录制类型，无法重启", zap.String("type", record.Type))
			}
			exception := Exception{
				AlarmType:  "Recording-Stop",
				AlarmDesc:  "Recording Stopped Exception",
				StreamPath: record.StreamPath,
				FileName:   record.Filename,
			}
			go sendExceptionCallback(&exception)
		}
	} else {
		plugin.Info("未发现已停止的录制流")
	}
}

// getActiveRecordingsFromDB retrieves all non-deleted recording events from the database.
func getActiveRecordingsFromDB() ([]EventRecord, error) {
	var eventRecords []EventRecord
	err := db.Where("is_delete = ?", "0").Find(&eventRecords).Error
	return eventRecords, err
}

// getActiveRecorders collects all active IRecorder instances from the sync.Map.
func (conf *RecordConfig) getActiveRecorders() map[string]IRecorder {
	recorders := make(map[string]IRecorder)
	conf.recordings.Range(func(key, value any) bool {
		if recorder, ok := value.(IRecorder); ok {
			recorders[recorder.GetRecorder().ID] = recorder
		}
		return true
	})
	return recorders
}

// findStoppedRecordings compares DB records with active recorders and returns the ones that have stopped.
func findStoppedRecordings(eventRecords []EventRecord, activeRecorders map[string]IRecorder) []EventRecord {
	var stopped []EventRecord
	for _, record := range eventRecords {
		if _, found := activeRecorders[record.RecId]; !found {
			stopped = append(stopped, record)
		}
	}
	return stopped
}

// sendExceptionCallback sends an exception notification to a third-party API and logs the event.
func sendExceptionCallback(exception *Exception) {
	exception.CreateTime = time.Now().Format("2006-01-02 15:04:05")
	exception.ServerIP = RecordPluginConfig.LocalIp

	if err := db.Create(&exception).Error; err != nil {
		plugin.Error("异常数据插入数据库失败", zap.Error(err))
		// Continue to attempt sending the callback
	}

	data, err := json.Marshal(exception)
	if err != nil {
		plugin.Error("序列化异常信息失败", zap.Error(err))
		return
	}

	resp, err := http.Post(RecordPluginConfig.ExceptionPostUrl, "application/json", bytes.NewBuffer(data))
	if err != nil {
		plugin.Error("发送异常信息到第三方API失败", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		plugin.Error("发送异常信息失败，第三方API返回状态码", zap.Int("statusCode", resp.StatusCode))
	} else {
		plugin.Info("成功发送异常信息", zap.String("alarmType", exception.AlarmType))
	}
}
