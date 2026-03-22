package record

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/minio/minio-go/v7"
	"go.uber.org/zap"
)

const (
	minioUploadFailedAlarmType = "minio upload failed"
	reuploadBatchSize          = 50
)

// runFailedUploadRetrier 启动 Minio 上传失败记录的定时重传任务
func (conf *RecordConfig) runFailedUploadRetrier() {
	// 启动时立即执行一次
	conf.retryFailedUploads()

	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		conf.retryFailedUploads()
	}
}

// retryFailedUploads 分批查询上传失败记录并重新上传
func (conf *RecordConfig) retryFailedUploads() {
	if db == nil || !conf.Storage.isConfigured() {
		return
	}

	plugin.Info("开始重传 Minio 上传失败的文件")
	totalRetried := 0

	for {
		var exceptions []Exception
		err := db.Where("alarm_type = ?", minioUploadFailedAlarmType).
			Limit(reuploadBatchSize).
			Find(&exceptions).Error
		if err != nil {
			plugin.Error("查询上传失败记录失败", zap.Error(err))
			return
		}
		if len(exceptions) == 0 {
			break
		}

		for _, exc := range exceptions {
			if conf.retryUploadSingle(exc) {
				totalRetried++
			}
		}
	}

	if totalRetried > 0 {
		plugin.Info("Minio 重传完成", zap.Int("successCount", totalRetried))
	}
}

// retryUploadSingle 重传单条失败记录，成功后删除异常记录
// 返回 true 表示重传成功
func (conf *RecordConfig) retryUploadSingle(exc Exception) bool {
	fileFullPath := filepath.Join(exc.FilePath, exc.FileName)

	// 本地文件已不存在，清除异常记录
	if _, err := os.Stat(fileFullPath); os.IsNotExist(err) {
		plugin.Warn("重传文件已不存在，删除异常记录",
			zap.Uint("excId", exc.Id),
			zap.String("file", fileFullPath))
		db.Delete(&exc)
		return false
	}

	maxRetries := conf.Storage.getMaxRetries()
	baseInterval := conf.Storage.getRetryInterval()

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(float64(baseInterval) * math.Pow(2, float64(attempt-1)))
			plugin.Warn("Minio重传重试",
				zap.Int("attempt", attempt),
				zap.Duration("backoff", backoff),
				zap.String("file", fileFullPath))
			time.Sleep(backoff)
		}

		lastErr = doStandaloneUpload(&conf.Storage, exc.FilePath, exc.FileName)
		if lastErr == nil {
			plugin.Info("Minio重传成功",
				zap.Uint("excId", exc.Id),
				zap.String("file", fileFullPath))
			// 上传成功，删除异常记录
			if err := db.Delete(&exc).Error; err != nil {
				plugin.Error("删除已重传的异常记录失败", zap.Uint("excId", exc.Id), zap.Error(err))
			}
			return true
		}

		plugin.Error("Minio重传失败",
			zap.Int("attempt", attempt),
			zap.String("file", fileFullPath),
			zap.Error(lastErr))
	}

	// 本轮重试全部失败，更新异常描述，等待下次定时重传
	db.Model(&exc).Update("alarm_desc",
		fmt.Sprintf("重传失败(重试%d次): %v", maxRetries, lastErr))
	return false
}

// doStandaloneUpload 不依赖 Recorder 实例的独立上传，用于重传场景
func doStandaloneUpload(storage *StorageConfig, filePath string, fileName string) error {
	timeout := storage.getUploadTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	minioClient, err := storage.getClient()
	if err != nil {
		return fmt.Errorf("创建MinioClient失败: %w", err)
	}

	region := storage.Region
	if region == "" {
		region = "us-east-1"
	}

	bucketName := storage.Bucket
	exists, err := minioClient.BucketExists(ctx, bucketName)
	if err != nil {
		return fmt.Errorf("检查Bucket存在性失败: %w", err)
	}
	if !exists {
		if err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: region}); err != nil {
			return fmt.Errorf("创建Bucket失败: %w", err)
		}
		plugin.Info("Successfully created Bucket", zap.String("bucket", bucketName))
	}

	fileFullPath := filepath.Join(filePath, fileName)
	putOpts := minio.PutObjectOptions{ContentType: "application/octet-stream"}
	if stat, statErr := os.Stat(fileFullPath); statErr == nil {
		putOpts.UserMetadata = map[string]string{
			"video-size-bytes": fmt.Sprintf("%d", stat.Size()),
		}
	}

	info, err := minioClient.FPutObject(ctx, bucketName, fileName, fileFullPath, putOpts)
	if err != nil {
		return fmt.Errorf("PutObject失败: %w", err)
	}

	plugin.Info("Successfully re-uploaded", zap.String("Key", info.Key), zap.Int64("Size", info.Size))
	return nil
}
