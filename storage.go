package record

import (
	"context"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/zap"
)

type StorageConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

func (r *Recorder) UploadFile(filePath string, fileName string) {
	ctx := context.Background()
	endpoint := r.Storage.Endpoint
	accessKeyID := r.Storage.AccessKey
	secretAccessKey := r.Storage.SecretKey
	bucketName := r.Storage.Bucket
	useSSL := r.Storage.UseSSL
	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})

	if err != nil {
		r.Error("create minioClient error:", zap.Error(err))
	}

	// Make a new bucket called testbucket.
	location := "us-east-1"

	err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: location})
	if err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, errBucketExists := minioClient.BucketExists(ctx, bucketName)
		if errBucketExists == nil && exists {

			r.Info("Bucket already Exists", zap.String("bucket", bucketName))
		} else {
			r.Error("Create Bucket Error:", zap.Error(err))
		}
	} else {
		r.Info("Successfully created Bucket:", zap.String("bucket", bucketName))
	}

	// Upload the test file
	// Change the value of filePath if the file is in another location
	objectName := fileName
	fileFullPath := filePath + "/" + objectName
	r.Info("Prepare Upload  Path:  fileName:", zap.String("filePath", filePath), zap.String("objectName", objectName))
	contentType := "application/octet-stream"

	// Upload the test file with FPutObject
	info, err := minioClient.FPutObject(ctx, bucketName, objectName, fileFullPath, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		r.Error("Minio PutObject Error:", zap.Error(err))
	}

	r.Info("Successfully uploaded of size ", zap.String("objectName", objectName), zap.Int64("Size", info.Size))
}
