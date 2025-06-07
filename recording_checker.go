package record

import (
	"fmt"
	"strings"
	"time"
)

// 查询所有正在录制中的记录
func (conf *RecordConfig) CheckRecordDB() {
	go func() {
		for {
			fmt.Println("打印record的内容 %d", time.Now().Format("2006-01-02 15:04:05"))
			var outRecordings []any
			var eventRecords []EventRecord

			var recordings []any
			conf.recordings.Range(func(key, value any) bool {
				recordings = append(recordings, value)
				return true
			})
			err = db.Where("is_delete = ?", "0").Find(&eventRecords).Error
			if err != nil {
				fmt.Println("查询数据库失败 %s", err.Error())
				return
			} else {
				fmt.Println("打印eventRecords的记录数量 %d", len(eventRecords))
				// 查询出在eventRecords中fileName字段包含recordings中的filename的记录
				// 遍历eventRecords，判断fileName字段是否包含recordings中的filename
				for _, record := range eventRecords {
					fmt.Println("打印record的内容 %d", record)
					for _, recording := range recordings {
						if strings.Contains(recording.(IRecorder).GetRecorder().ID, record.Filename) {

						} else {
							outRecordings = append(outRecordings, recording)
						}
					}
				}
				// 打印outRecordings的内容
				for _, outRecording := range outRecordings {
					fmt.Println("打印outRecordings的内容 %d", outRecording)
				}
			}

			// 等待 1 分钟后继续执行
			<-time.After(10 * time.Second)
		}
	}()

}
