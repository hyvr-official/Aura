package backups

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func upload(ctx context.Context, b Backup, file string) (string, error) {
	switch b.Source {
	case SourceAWSS3:
		return uploadS3(ctx, b, file)
	case SourceLocal:
		return uploadLocal(b, file)
	default:
		return "", fmt.Errorf("unsupported source %q", b.Source)
	}
}

func prune(ctx context.Context, b Backup) ([]string, error) {
	switch b.Source {
	case SourceAWSS3:
		return pruneS3(ctx, b)
	case SourceLocal:
		return pruneLocal(b)
	default:
		return nil, fmt.Errorf("unsupported source %q", b.Source)
	}
}

func localDir(b Backup) string {
	return filepath.Join(b.LocalPath, b.SourceFolder)
}

func uploadLocal(b Backup, file string) (string, error) {
	dir := localDir(b)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create local folder: %w", err)
	}
	dest := filepath.Join(dir, filepath.Base(file))
	if err := os.Rename(file, dest); err == nil {
		return dest, nil
	}
	if err := copyFile(file, dest); err != nil {
		return "", err
	}
	os.Remove(file)
	return dest, nil
}

func copyFile(src, dest string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	part := dest + ".part"
	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			out.Close()
			os.Remove(part)
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(part, dest)
}

func pruneLocal(b Backup) ([]string, error) {
	dir := localDir(b)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list local folder: %w", err)
	}
	type file struct {
		path     string
		modified time.Time
	}
	var files []file
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !ownsArchive(b, name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{path: filepath.Join(dir, name), modified: info.ModTime()})
	}

	if len(files) <= b.LimitCount {
		return nil, nil
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modified.After(files[j].modified)
	})

	var removed []string
	for _, f := range files[b.LimitCount:] {
		if err := os.Remove(f.path); err != nil {
			return removed, fmt.Errorf("delete %s: %w", f.path, err)
		}
		removed = append(removed, f.path)
	}
	return removed, nil
}

func s3Client(ctx context.Context, b Backup) (*s3.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(b.S3Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(b.S3AccessKey, b.S3Secret, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return s3.NewFromConfig(cfg), nil
}

func uploadS3(ctx context.Context, b Backup, file string) (string, error) {
	client, err := s3Client(ctx, b)
	if err != nil {
		return "", err
	}

	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()

	key := path.Join(b.SourceFolder, filepath.Base(file))
	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.Concurrency = 1
	})
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(b.S3Bucket),
		Key:          aws.String(key),
		Body:         f,
		StorageClass: types.StorageClass(b.S3StorageClass),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("s3://%s/%s", b.S3Bucket, key), nil
}

func pruneS3(ctx context.Context, b Backup) ([]string, error) {
	client, err := s3Client(ctx, b)
	if err != nil {
		return nil, err
	}

	prefix := ""
	if b.SourceFolder != "" {
		prefix = b.SourceFolder + "/"
	}
	type object struct {
		key      string
		modified time.Time
	}
	var objects []object
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(b.S3Bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list objects: %w", err)
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			if !ownsArchive(b, path.Base(key)) {
				continue
			}
			objects = append(objects, object{key: key, modified: aws.ToTime(o.LastModified)})
		}
	}

	if len(objects) <= b.LimitCount {
		return nil, nil
	}
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].modified.After(objects[j].modified)
	})

	var removed []string
	for _, o := range objects[b.LimitCount:] {
		_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(b.S3Bucket),
			Key:    aws.String(o.key),
		})
		if err != nil {
			return removed, fmt.Errorf("delete %s: %w", o.key, err)
		}
		removed = append(removed, fmt.Sprintf("s3://%s/%s", b.S3Bucket, o.key))
	}
	return removed, nil
}
