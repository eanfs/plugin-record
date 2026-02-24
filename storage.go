package record

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

var uploadSemaphore = make(chan struct{}, 8)

type StorageConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string // 可配置 region，默认 us-east-1
	UseSSL    bool

	mu     sync.Mutex
	client *minio.Client
}

func (s *StorageConfig) isConfigured() bool {
	return s.Endpoint != "" && s.SecretKey != "" && s.AccessKey != "" && s.Bucket != ""
}

// getClient 懒初始化并复用 minio client，线程安全
func (s *StorageConfig) getClient() (*minio.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	client, err := minio.New(s.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(s.AccessKey, s.SecretKey, ""),
		Secure: s.UseSSL,
	})
	if err != nil {
		return nil, err
	}
	s.client = client
	return s.client, nil
}

func (r *Recorder) UploadFile(filePath string, fileName string) {
	r.UploadFileWithTags(filePath, fileName, 0)
}

func (r *Recorder) UploadFileWithTags(filePath string, fileName string, durationMs uint32) {
	// 先检查配置，避免无效占用信号量
	if !r.Storage.isConfigured() {
		r.Info("Minio Storage Config Not Configured")
		return
	}

	uploadSemaphore <- struct{}{}
	defer func() { <-uploadSemaphore }()

	ctx := context.Background()

	minioClient, err := r.Storage.getClient()
	if err != nil {
		r.Error("create minioClient error", zap.Error(err))
		return
	}

	region := r.Storage.Region
	if region == "" {
		region = "us-east-1"
	}

	bucketName := r.Storage.Bucket
	exists, err := minioClient.BucketExists(ctx, bucketName)
	if err != nil {
		r.Error("Failed to check bucket existence", zap.Error(err))
		return
	}
	if !exists {
		if err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: region}); err != nil {
			r.Error("Create Bucket Error", zap.Error(err))
			return
		}
		r.Info("Successfully created Bucket", zap.String("bucket", bucketName))
	}

	fileFullPath := filepath.Join(filePath, fileName)
	putOpts := minio.PutObjectOptions{ContentType: "application/octet-stream"}
	if stat, statErr := os.Stat(fileFullPath); statErr == nil {
		putOpts.UserMetadata = map[string]string{
			"video-size-bytes": fmt.Sprintf("%d", stat.Size()),
		}
		if durationMs > 0 {
			putOpts.UserMetadata["video-duration-ms"] = fmt.Sprintf("%d", durationMs)
		}
	} else {
		r.Warn("get file stat before upload failed", zap.Error(statErr), zap.String("file", fileFullPath))
	}

	info, err := minioClient.FPutObject(ctx, bucketName, fileName, fileFullPath, putOpts)
	if err != nil {
		r.Error("Minio PutObject Error", zap.Error(err))
		return
	}

	r.Info("Successfully uploaded", zap.String("Key", info.Key), zap.Int64("Size", info.Size))
	r.RemoveRecordById()
}
