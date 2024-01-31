package record

import (
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type StorageConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

func (r *Recorder) UploadFile(filePath string, fileName string) {
	fmt.Println(fileName)
	ctx := context.Background()
	fmt.Println(r.Storage.Endpoint)

	endpoint := r.Storage.Endpoint
	accessKeyID := r.Storage.AccessKey
	secretAccessKey := r.Storage.SecretKey
	bucketName := r.Storage.Bucket
	useSSL := r.Storage.UseSSL
	fmt.Println(r.Storage.AccessKey)
	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})

	if err != nil {
		fmt.Println(err.Error())
	}

	// Make a new bucket called testbucket.
	location := "us-east-1"

	err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: location})
	if err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, errBucketExists := minioClient.BucketExists(ctx, bucketName)
		if errBucketExists == nil && exists {
			fmt.Println("We already own %s\n", bucketName)
		} else {
			fmt.Println(err.Error())
		}
	} else {
		fmt.Println("Successfully created %s\n", bucketName)
	}

	// Upload the test file
	// Change the value of filePath if the file is in another location
	objectName := fileName
	fileFullPath := filePath + "/" + objectName
	fmt.Println("Prepare Upload  Path: %s  fileName: %s", fileFullPath, objectName)
	contentType := "application/octet-stream"

	// Upload the test file with FPutObject
	info, err := minioClient.FPutObject(ctx, bucketName, objectName, fileFullPath, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		fmt.Println(err.Error())
	}

	fmt.Println("Successfully uploaded %s of size %d\n", objectName, info.Size)
}
