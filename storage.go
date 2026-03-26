package record

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

type StorageConfig struct {
	Endpoint             string        `desc:"Minio 服务端点"`
	AccessKey            string        `desc:"Minio 访问密钥"`
	SecretKey            string        `desc:"Minio 秘密密钥"`
	Bucket               string        `desc:"Minio 存储桶"`
	Region               string        `desc:"Minio region，默认 us-east-1"`
	UseSSL               bool          `desc:"是否使用 SSL 连接"`
	MaxConcurrentUploads int           `desc:"最大并发上传数量，默认 4"`
	UploadTimeout        time.Duration `desc:"单次上传超时时间，默认 15m"`
	ConnectTimeout       time.Duration `desc:"TCP连接超时时间，默认 10s"`
	MaxRetries           int           `desc:"上传失败最大重试次数，默认 3"`
	RetryInterval        time.Duration `desc:"重试基础间隔（指数退避），默认 10s"`

	mu              sync.Mutex
	client          *minio.Client
	uploadSemaphore chan struct{}
	semaphoreOnce   sync.Once
}

func (s *StorageConfig) isConfigured() bool {
	return s.Endpoint != "" && s.SecretKey != "" && s.AccessKey != "" && s.Bucket != ""
}

// getUploadSemaphore 懒初始化并发信号量，线程安全
func (s *StorageConfig) getUploadSemaphore() chan struct{} {
	s.semaphoreOnce.Do(func() {
		size := s.MaxConcurrentUploads
		if size <= 0 {
			size = 4
		}
		s.uploadSemaphore = make(chan struct{}, size)
	})
	return s.uploadSemaphore
}

// getUploadTimeout 获取上传超时时间，默认 30 分钟
func (s *StorageConfig) getUploadTimeout() time.Duration {
	if s.UploadTimeout > 0 {
		return s.UploadTimeout
	}
	return 15 * time.Minute
}

// getMaxRetries 获取最大重试次数，默认 3
func (s *StorageConfig) getMaxRetries() int {
	if s.MaxRetries > 0 {
		return s.MaxRetries
	}
	return 3
}

// getRetryInterval 获取重试基础间隔，默认 10 秒
func (s *StorageConfig) getRetryInterval() time.Duration {
	if s.RetryInterval > 0 {
		return s.RetryInterval
	}
	return 10 * time.Second
}

// getConnectTimeout 获取 TCP 连接超时时间，默认 10 秒
func (s *StorageConfig) getConnectTimeout() time.Duration {
	if s.ConnectTimeout > 0 {
		return s.ConnectTimeout
	}
	return 10 * time.Second
}

// newTransport 创建带超时配置的 HTTP Transport
func (s *StorageConfig) newTransport() http.RoundTripper {
	connectTimeout := s.getConnectTimeout()
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   connectTimeout,     // TCP 连接超时
			KeepAlive: 30 * time.Second,   // TCP keepalive 间隔
		}).DialContext,
		TLSHandshakeTimeout:   connectTimeout,     // TLS 握手超时
		ResponseHeaderTimeout: 60 * time.Second,    // 等待响应头超时
		IdleConnTimeout:       90 * time.Second,    // 空闲连接回收时间
		MaxIdleConnsPerHost:   s.getMaxIdleConns(),
		ExpectContinueTimeout: 5 * time.Second,     // 100-continue 超时
		TLSClientConfig:       &tls.Config{},
	}
}

// getMaxIdleConns 空闲连接数与并发上传数一致
func (s *StorageConfig) getMaxIdleConns() int {
	size := s.MaxConcurrentUploads
	if size <= 0 {
		size = 4
	}
	return size
}

// getClient 懒初始化并复用 minio client，线程安全
func (s *StorageConfig) getClient() (*minio.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	client, err := minio.New(s.Endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(s.AccessKey, s.SecretKey, ""),
		Secure:    s.UseSSL,
		Transport: s.newTransport(),
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

	// 并发限流：获取信号量
	semaphore := r.Storage.getUploadSemaphore()
	semaphore <- struct{}{}
	defer func() { <-semaphore }()

	fileFullPath := filepath.Join(filePath, fileName)
	streamPath := ""
	if r.Stream != nil {
		streamPath = r.Stream.Path
	}

	maxRetries := r.Storage.getMaxRetries()
	baseInterval := r.Storage.getRetryInterval()

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避：baseInterval * 2^(attempt-1)
			backoff := time.Duration(float64(baseInterval) * math.Pow(2, float64(attempt-1)))
			r.Warn("Minio上传重试",
				zap.Int("attempt", attempt),
				zap.Int("maxRetries", maxRetries),
				zap.Duration("backoff", backoff),
				zap.String("file", fileFullPath),
			)
			time.Sleep(backoff)
		}

		lastErr = r.doUpload(filePath, fileName, durationMs)
		if lastErr == nil {
			if attempt > 0 {
				r.Info("Minio上传重试成功",
					zap.Int("attempt", attempt),
					zap.String("file", fileFullPath),
				)
			}
			return
		}

		r.Error("Minio上传失败",
			zap.Int("attempt", attempt),
			zap.Error(lastErr),
			zap.String("file", fileFullPath),
		)
	}

	// 所有重试用尽，记录异常到数据库
	r.Error("Minio上传最终失败，已用尽所有重试",
		zap.Int("maxRetries", maxRetries),
		zap.Error(lastErr),
		zap.String("file", fileFullPath),
	)
	saveUploadException(streamPath, filePath, fileName, maxRetries, lastErr)
}

// doUpload 执行单次 Minio 上传，带超时控制
func (r *Recorder) doUpload(filePath string, fileName string, durationMs uint32) error {
	timeout := r.Storage.getUploadTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	minioClient, err := r.Storage.getClient()
	if err != nil {
		return fmt.Errorf("创建MinioClient失败: %w", err)
	}

	region := r.Storage.Region
	if region == "" {
		region = "us-east-1"
	}

	bucketName := r.Storage.Bucket
	exists, err := minioClient.BucketExists(ctx, bucketName)
	if err != nil {
		return fmt.Errorf("检查Bucket存在性失败: %w", err)
	}
	if !exists {
		if err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: region}); err != nil {
			return fmt.Errorf("创建Bucket失败: %w", err)
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
		return fmt.Errorf("PutObject失败: %w", err)
	}

	r.Info("Successfully uploaded", zap.String("Key", info.Key), zap.Int64("Size", info.Size))
	r.RemoveRecordById()
	return nil
}

// saveUploadException 将上传失败异常记录到数据库
func saveUploadException(streamPath string, filePath string, fileName string, maxRetries int, lastErr error) {
	if db == nil {
		return
	}
	exception := &Exception{
		CreateTime: time.Now().Format("2006-01-02 15:04:05"),
		AlarmType:  "minio upload failed",
		AlarmDesc:  fmt.Sprintf("上传失败(重试%d次): %v", maxRetries, lastErr),
		StreamPath: streamPath,
		FileName:   fileName,
		FilePath:   filePath,
		ServerIP:   RecordPluginConfig.LocalIp,
	}
	if err := db.Create(exception).Error; err != nil {
		plugin.Error("上传异常记录写入数据库失败", zap.Error(err))
	}
}
