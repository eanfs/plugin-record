package record

import (
	_ "embed"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/config"
	"m7s.live/engine/v4/util"
)

type RecordConfig struct {
	config.Subscribe
	config.HTTP
	Flv                         Record `desc:"flv录制配置"`
	Mp4                         Record `desc:"mp4录制配置"`
	Fmp4                        Record `desc:"fmp4录制配置"`
	Hls                         Record `desc:"hls录制配置"`
	Raw                         Record `desc:"视频裸流录制配置"`
	RawAudio                    Record `desc:"音频裸流录制配置"`
	recordings                  sync.Map
	beforeDuration              int           `desc:"事件前缓存时长"`
	afterDuration               int           `desc:"事件后缓存时长"`
	MysqlDSN                    string        `desc:"mysql数据库连接字符串"`
	SqliteDbPath                string        `desc:"sqlite数据库路径"`
	DiskMaxPercent              float64       `desc:"硬盘使用百分之上限值，超过后报警"`
	LocalIp                     string        `desc:"本机IP"`
	RecordFileExpireDays        int           `desc:"录像自动删除的天数,0或未设置表示不自动删除"`
	RecordPathNotShowStreamPath bool          `desc:"录像路径中是否包含streamPath，默认true"`
	Storage                     StorageConfig `desc:"MINIO 配置"`
	expireCleanerOnce           sync.Once     // 确保过期清理协程只启动一次
	uploadRetrierOnce           sync.Once     // 确保上传重传协程只启动一次
}

//go:embed default.yaml
var defaultYaml DefaultYaml
var ErrRecordExist = errors.New("recorder exist")
var RecordPluginConfig = &RecordConfig{
	Flv: Record{
		Path:          "record/flv",
		Ext:           ".flv",
		GetDurationFn: getFLVDuration,
	},
	Fmp4: Record{
		Path: "record/fmp4",
		Ext:  ".mp4",
	},
	Mp4: Record{
		Path: "record/mp4",
		Ext:  ".mp4",
	},
	Hls: Record{
		Path: "record/hls",
		Ext:  ".m3u8",
	},
	Raw: Record{
		Path: "record/raw",
		Ext:  ".", // 默认h264扩展名为.h264,h265扩展名为.h265
	},
	RawAudio: Record{
		Path: "record/raw",
		Ext:  ".", // 默认aac扩展名为.aac,pcma扩展名为.pcma,pcmu扩展名为.pcmu
	},
	beforeDuration:              30,
	afterDuration:               30,
	MysqlDSN:                    "",
	SqliteDbPath:                "./m7sv4.db",
	DiskMaxPercent:              80.00,
	LocalIp:                     getLocalIP(),
	RecordFileExpireDays:        0,
	RecordPathNotShowStreamPath: true,
}

var plugin = InstallPlugin(RecordPluginConfig, defaultYaml)
var db *gorm.DB

func (conf *RecordConfig) OnEvent(event any) {
	switch v := event.(type) {
	case FirstConfig, config.Config:
		if conf.MysqlDSN == "" {
			plugin.Info("sqliteDb filepath is" + conf.SqliteDbPath)
			db = initSqliteDB(conf.SqliteDbPath)
		} else {
			plugin.Info("mysqlDSN is" + conf.MysqlDSN)
			db = initMysqlDB(conf.MysqlDSN)
		}

		if conf.RecordFileExpireDays > 0 {
			conf.expireCleanerOnce.Do(func() {
				go conf.runExpiredRecordCleaner()
			})
		}
		// Minio 上传失败文件重传
		if conf.Storage.isConfigured() {
			conf.uploadRetrierOnce.Do(func() {
				go conf.runFailedUploadRetrier()
			})
		}
		//检查录像任务是否存在，不存在则启动
		conf.CheckRecordDB()

		conf.Flv.Init()
		conf.Mp4.Init()
		conf.Fmp4.Init()
		conf.Hls.Init()
		conf.Raw.Init()
		conf.RawAudio.Init()
	case SEpublish:
		streamPath := v.Target.Path
		if conf.Flv.NeedRecord(streamPath) {
			go NewFLVRecorder(OrdinaryMode).Start(streamPath)
		}
		if conf.Mp4.NeedRecord(streamPath) {
			go NewMP4Recorder().Start(streamPath)
		}
		if conf.Fmp4.NeedRecord(streamPath) {
			go NewFMP4Recorder().Start(streamPath)
		}
		if conf.Hls.NeedRecord(streamPath) {
			go NewHLSRecorder().Start(streamPath)
		}
		if conf.Raw.NeedRecord(streamPath) {
			go NewRawRecorder().Start(streamPath)
		}
		if conf.RawAudio.NeedRecord(streamPath) {
			go NewRawAudioRecorder().Start(streamPath)
		}
	}
}
func (conf *RecordConfig) getRecorderConfigByType(t string) (recorder *Record) {
	switch t {
	case "flv":
		recorder = &conf.Flv
	case "mp4":
		recorder = &conf.Mp4
	case "fmp4":
		recorder = &conf.Fmp4
	case "hls":
		recorder = &conf.Hls
	case "raw":
		recorder = &conf.Raw
	case "raw_audio":
		recorder = &conf.RawAudio
	}
	return
}

func getFLVDuration(file io.ReadSeeker) uint32 {
	_, err := file.Seek(-4, io.SeekEnd)
	if err == nil {
		var tagSize uint32
		if tagSize, err = util.ReadByteToUint32(file, true); err == nil {
			_, err = file.Seek(-int64(tagSize)-4, io.SeekEnd)
			if err == nil {
				_, timestamp, _, err := codec.ReadFLVTag(file)
				if err == nil {
					return timestamp
				}
			}
		}
	}
	return 0
}

const expiredRecordBatchSize = 100 // 每批处理的过期记录数量

// runExpiredRecordCleaner 启动过期录像文件的定时清理任务。
// 仅清理 event_level='1'（非重要）且未被删除（is_delete='0'）的记录。
// 磁盘文件删除后，将数据库记录标记为软删除（is_delete='1'）。
func (conf *RecordConfig) runExpiredRecordCleaner() {
	// 启动时立即执行一次清理
	conf.cleanExpiredRecords()

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		conf.cleanExpiredRecords()
	}
}

// cleanExpiredRecords 分批查询并清理过期的非重要录像记录
func (conf *RecordConfig) cleanExpiredRecords() {
	expireTime := time.Now().AddDate(0, 0, -conf.RecordFileExpireDays)
	plugin.Info("开始清理过期录像文件",
		zap.Int("expireDays", conf.RecordFileExpireDays),
		zap.String("expireTime", expireTime.Format("2006-01-02 15:04:05")))

	totalCleaned := 0
	for {
		var records []EventRecord
		err := db.Where("event_level = ? AND is_delete = ? AND create_time < ?", "1", "0", expireTime).
			Limit(expiredRecordBatchSize).
			Find(&records).Error
		if err != nil {
			plugin.Error("查询过期录像失败", zap.Error(err))
			return
		}
		if len(records) == 0 {
			break
		}
		for _, record := range records {
			conf.deleteExpiredRecord(record)
		}
		totalCleaned += len(records)
	}

	if totalCleaned > 0 {
		plugin.Info("过期录像清理完成", zap.Int("cleanedCount", totalCleaned))
	}
}

// deleteExpiredRecord 删除单条过期录像：先删磁盘文件，再软删除数据库记录
func (conf *RecordConfig) deleteExpiredRecord(record EventRecord) {
	if err := os.Remove(record.Filepath); err != nil && !os.IsNotExist(err) {
		plugin.Error("删除录像文件失败",
			zap.String("recId", record.RecId),
			zap.String("filepath", record.Filepath),
			zap.Error(err))
		return
	}
	// 软删除：标记 is_delete='1'
	if err := db.Model(&record).Update("is_delete", "1").Error; err != nil {
		plugin.Error("更新数据库记录失败",
			zap.String("recId", record.RecId),
			zap.Error(err))
	}
}

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}

	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return ""
}
